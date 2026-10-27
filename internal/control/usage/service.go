// Package usage implements the account console's money views:
//
//	GET  /api/console/usage              aggregated usage by model/channel/day
//	GET  /api/console/bills              one CST calendar month's bill
//	POST /api/console/usage/export       start an asynchronous CSV export
//	GET  /api/console/exports/{token}    task status (task token) or CSV (download token)
//	GET  /api/console/usage/metrics      realtime (Redis K14/K15) vs settled (t_metric_daily)
//
// Three rules shape every file here:
//
//   - Usage separates counting from billing. request_total and success_rate_bp
//     cover every row including FAILED; tokens and money sum result = 'SUCCESS'
//     rows only (T14 hard constraint 6 / A12). A success rate over SUCCESS-only
//     rows would always be 10000. The log list's filter is deliberately a
//     separate piece of code in logq: sharing one default would make one of
//     the two views wrong.
//   - Money never touches a float. Aggregates are SUM()'d in MySQL and scanned
//     into int64; micro-credits become fen with (micro + 5000) / 10000, and a
//     fen amount becomes a yuan string with exactly two decimals.
//   - Every query is scoped by user_id = session.uid. Filtering by model_id or
//     channel_id never widens that scope, and switching dimension only changes
//     the GROUP BY, never the WHERE (F9-1 / A2).
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

const (
	// onlineWindow is the console's online query window, not a retention
	// period. A from older than this answers 400 with "use archive export"
	// instead of silently returning a short result (T14 hard constraint 2).
	onlineWindow = 365 * 24 * time.Hour

	// defaultWindow is the usage/bill default when from is omitted.
	defaultWindow = 7 * 24 * time.Hour

	// realtimeBuckets is how many one-minute K14/K15 buckets the realtime
	// view reads. Keys live 300s (settle.MetricTTL), so buckets older than
	// five minutes are usually already gone; summing 60 buckets is still
	// correct and simply discovers absent keys as zero.
	realtimeBuckets = 60

	// realtimeLagSeconds documents the display delay: settle flushes once per
	// second into per-minute buckets, so the freshest bucket is an incomplete
	// minute. The response says so rather than pretending the number is live.
	realtimeLagSeconds = 60

	// defaultExportTTL is the download-token lifetime (T14 A11).
	defaultExportTTL = time.Hour

	// exportTimeout bounds the background CSV job. The request context is
	// detached with context.WithoutCancel so returning 202 cannot kill the
	// worker, and this timeout keeps an orphaned job from running forever.
	exportTimeout = 60 * time.Second
)

// cst is UTC+8 without daylight saving. Asia/Shanghai has observed no DST
// since 1991, and a fixed zone avoids requiring tzdata on the Windows test
// host (the same convention as internal/recon).
var cst = time.FixedZone("CST", 8*3600)

// Service owns the usage reads, the export task and the metrics comparison.
type Service struct {
	DB     *sql.DB
	Redis  *redisx.Client
	Now    clock.Clock
	Logger *obslog.Logger

	// Exporter is the file seam. FileExporter is the default; tests replace it
	// to observe completion without touching the real filesystem.
	Exporter Exporter

	// HMACKey is EXPORT_HMAC_KEY from the process environment. Empty means
	// StartExport answers 503: an unsigned download URL is never a fallback.
	HMACKey []byte

	// ExportTTL overrides the one-hour token lifetime in tests. Zero uses
	// defaultExportTTL.
	ExportTTL time.Duration
}

// New wires the service. Export parameters come from the environment, not
// from t_config_item: EXPORT_DIR (default os.TempDir()/yunshu-exports) and
// EXPORT_HMAC_KEY. The 45 frozen configuration keys intentionally carry no
// file-system or signing secret.
func New(db *sql.DB, rdb *redisx.Client, now clock.Clock, logger *obslog.Logger) *Service {
	if now == nil {
		now = clock.Real{}
	}
	dir := strings.TrimSpace(os.Getenv("EXPORT_DIR"))
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "yunshu-exports")
	}
	return &Service{
		DB:       db,
		Redis:    rdb,
		Now:      now,
		Logger:   logger,
		Exporter: &FileExporter{Dir: dir},
		HMACKey:  []byte(os.Getenv("EXPORT_HMAC_KEY")),
	}
}

func (s *Service) now() time.Time { return s.Now.Now().UTC() }

func (s *Service) exportTTL() time.Duration {
	if s.ExportTTL > 0 {
		return s.ExportTTL
	}
	return defaultExportTTL
}

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "usage", Status: 500})
	}
}

func (s *Service) logWarn(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Warn(ctx, msg, obslog.Fields{Stage: "usage"})
	}
}

// fenFromMicro converts micro-credits to fen with half-up rounding, using
// integers only. 1 credit = 10,000 micro; 1 yuan = 100 credit = 1,000,000
// micro. Therefore 12,345 credit = 123,450,000 micro → 12,345 fen → "123.45".
func fenFromMicro(micro int64) int64 {
	if micro >= 0 {
		return (micro + 5_000) / 10_000
	}
	return -((-micro + 5_000) / 10_000)
}

// yuanFromFen renders fen with exactly two decimals. The sign is handled
// separately so -5 fen is "-0.05", never "0.-5".
func yuanFromFen(fen int64) string {
	sign := ""
	if fen < 0 {
		sign = "-"
		fen = -fen
	}
	return sign + strconv.FormatInt(fen/100, 10) + "." +
		fmt.Sprintf("%02d", fen%100)
}

// successRateBP returns the rate in basis points (10000 = 100%), truncated:
// 2 successes out of 3 requests is 6666, not 6667. Truncation keeps the
// displayed rate a lower bound — it never rounds a failure up into a success.
func successRateBP(success, total int64) int64 {
	if total <= 0 {
		return 0
	}
	return success * 10_000 / total
}

// usageFilter is the parsed query. From/To are UTC instants; the CST boundary
// only enters through the bill and the day dimension.
type usageFilter struct {
	From      time.Time
	To        time.Time
	Dimension string
	ModelID   *int64
	ChannelID *int64
}

// parseUsageFilter reads from/to/dimension/model_id/channel_id. The default
// range is the last seven days and the default dimension is "model".
func (s *Service) parseUsageFilter(q url.Values) (usageFilter, *apierr.APIError) {
	now := s.now()
	to := now
	from := now.Add(-defaultWindow)

	if raw := strings.TrimSpace(q.Get("from")); raw != "" {
		v, _, ok := parseTimestamp(raw)
		if !ok {
			return usageFilter{}, mw.InvalidQueryField("from")
		}
		from = v
	}
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		v, dateOnly, ok := parseTimestamp(raw)
		if !ok {
			return usageFilter{}, mw.InvalidQueryField("to")
		}
		if dateOnly {
			v = v.Add(24*time.Hour - time.Millisecond)
		}
		to = v
	}
	if from.After(to) {
		return usageFilter{}, mw.InvalidQueryField("from")
	}
	if from.Before(now.Add(-onlineWindow)) {
		e := apierr.New(apierr.CodeInvalidQueryParameter)
		e.Message = "查询范围超出 365 天在线窗口"
		e = e.WithHint("请使用归档导出获取更早的用量数据")
		return usageFilter{}, e
	}
	if to.Sub(from) > onlineWindow {
		return usageFilter{}, mw.InvalidQueryField("to")
	}

	dimension := strings.ToLower(strings.TrimSpace(q.Get("dimension")))
	if dimension == "" {
		dimension = "model"
	}
	switch dimension {
	case "model", "channel", "day":
	default:
		return usageFilter{}, mw.InvalidQueryField("dimension")
	}

	modelID, apiErr := parseOptionalID(q.Get("model_id"), "model_id")
	if apiErr != nil {
		return usageFilter{}, apiErr
	}
	channelID, apiErr := parseOptionalID(q.Get("channel_id"), "channel_id")
	if apiErr != nil {
		return usageFilter{}, apiErr
	}

	return usageFilter{
		From: from.UTC(), To: to.UTC(), Dimension: dimension,
		ModelID: modelID, ChannelID: channelID,
	}, nil
}

func parseOptionalID(raw, field string) (*int64, *apierr.APIError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, mw.InvalidQueryField(field)
	}
	return &v, nil
}

func parseTimestamp(raw string) (t time.Time, dateOnly bool, ok bool) {
	if v, err := time.Parse(time.RFC3339, raw); err == nil {
		return v.UTC(), false, true
	}
	if v, err := time.Parse("2006-01-02", raw); err == nil {
		return v.UTC(), true, true
	}
	return time.Time{}, false, false
}

// sessionUID is the only source of the account scope. The route table already
// requires a session; a missing one here is a wiring bug answered as 401.
func sessionUID(r *http.Request) (int64, *apierr.APIError) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok || sess == nil {
		return 0, mw.SessionMissing()
	}
	return sess.UID, nil
}
