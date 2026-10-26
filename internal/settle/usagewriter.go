package settle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// P28/P29 batching. A full batch or the 3s ticker triggers a flush.
const (
	DefaultFlushInterval = 3 * time.Second
	DefaultFlushBatch    = 500
	// usageQueueSize bounds memory when the database is slow. A full queue
	// drops log rows; it never blocks the request and never touches money.
	usageQueueSize = 4096
)

// UsageRecord is one t_usage_record row (P28). IDs of 0 mean SQL NULL.
type UsageRecord struct {
	RequestID     string
	UserID        int64
	APIKeyID      int64
	APIKeyName    string
	ModelID       int64
	ModelName     string
	RequestModel  string
	InboundProto  string
	ChannelID     int64
	ChannelName   string
	OutboundProto string
	RouteID       int64
	PriceRuleID   int64
	ConfigVersion int64
	GroupCode     string
	PriceRatioBP  int64
	BillingTier   string
	IsStream      bool
	HTTPStatus    int
	BizCode       string
	Result        string
	FailStage     string
	FailOwner     string
	TokensIn      int64
	TokensOut     int64
	TokensInEst   int64
	TokensOutEst  int64
	UsageSource   string
	ImageCount    int64
	PreholdMicro  int64
	ConsumedMicro int64
	// ConsumedRechargeMicro / ConsumedBonusMicro are the split of
	// ConsumedMicro; the first delivery of T09 leaves them 0 and T04's ledger
	// result is the authority.
	ConsumedRechargeMicro int64
	ConsumedBonusMicro    int64
	RefundedMicro         int64
	UpstreamAttempts      int64
	TTFTMS                *int64
	TotalMS               int64
	UpstreamMS            int64
	ClientIP              []byte
	UserAgent             string
	ClientAborted         bool
	CreatedAt             time.Time
}

// AttemptRecord is one t_upstream_attempt row (P29).
type AttemptRecord struct {
	RequestID     string
	AttemptIndex  int
	UserID        int64
	ModelID       int64
	RouteID       int64
	ChannelID     int64
	ChannelName   string
	ProtocolCode  string
	UpstreamModel string
	Phase         string
	Result        string
	HTTPStatus    int
	UpstreamCode  string
	UpstreamMsg   string
	IsRetryable   bool
	RetryDecision string
	ConnectMS     int64
	FirstByteMS   int64
	TotalMS       int64
	TokensIn      int64
	TokensOut     int64
	BytesIn       int64
	BytesOut      int64
	ErrorClass    string
	CreatedAt     time.Time
}

// UsageWriter is the asynchronous P28/P29 writer.
//
// 丢日志不丢资金：WriteUsage/WriteAttempt never block and never return an error
// to the request path. A failed flush is logged at WARN and the batch is
// dropped; the settlement already committed in t_ledger_entry is untouched.
type UsageWriter struct {
	DB            *sql.DB
	Logger        *obslog.Logger
	Clock         clock.Clock
	FlushInterval time.Duration
	BatchSize     int

	usageCh   chan UsageRecord
	attemptCh chan AttemptRecord

	written atomic.Int64
	dropped atomic.Int64
}

// NewUsageWriter builds the writer.
func NewUsageWriter(db *sql.DB, logger *obslog.Logger, clk clock.Clock) *UsageWriter {
	if clk == nil {
		clk = clock.Real{}
	}
	return &UsageWriter{
		DB:            db,
		Logger:        logger,
		Clock:         clk,
		FlushInterval: DefaultFlushInterval,
		BatchSize:     DefaultFlushBatch,
		usageCh:       make(chan UsageRecord, usageQueueSize),
		attemptCh:     make(chan AttemptRecord, usageQueueSize),
	}
}

// WriteUsage enqueues one request-level row.
func (w *UsageWriter) WriteUsage(r UsageRecord) {
	if w == nil {
		return
	}
	select {
	case w.usageCh <- r:
	default:
		w.drop("usage")
	}
}

// WriteAttempt enqueues one attempt-level row.
func (w *UsageWriter) WriteAttempt(r AttemptRecord) {
	if w == nil {
		return
	}
	select {
	case w.attemptCh <- r:
	default:
		w.drop("attempt")
	}
}

// Stats returns flushed and dropped row counts.
func (w *UsageWriter) Stats() (written, dropped int64) {
	if w == nil {
		return 0, 0
	}
	return w.written.Load(), w.dropped.Load()
}

// Run flushes every FlushInterval until ctx is cancelled. The final flush uses
// a detached context so a shutdown does not discard rows already accepted.
func (w *UsageWriter) Run(ctx context.Context) error {
	if w == nil || w.DB == nil {
		return nil
	}
	interval := w.FlushInterval
	if interval <= 0 {
		interval = DefaultFlushInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = w.Flush(finalCtx)
			cancel()
			return nil
		case <-ticker.C:
			if err := w.Flush(ctx); err != nil && w.Logger != nil {
				w.Logger.Warn(ctx, "usage_flush_failed: "+err.Error(), obslog.Fields{Stage: "P28"})
			}
		}
	}
}

// Flush drains both queues in batches of BatchSize.
func (w *UsageWriter) Flush(ctx context.Context) error {
	if w == nil || w.DB == nil {
		return nil
	}
	usage := w.drainUsage()
	attempts := w.drainAttempts()
	var firstErr error
	for start := 0; start < len(usage); start += w.batchSize() {
		end := start + w.batchSize()
		if end > len(usage) {
			end = len(usage)
		}
		if err := w.insertUsage(ctx, usage[start:end]); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if w.Logger != nil {
				w.Logger.Warn(ctx, "usage_insert_failed: "+err.Error(), obslog.Fields{Stage: "P28"})
			}
			continue
		}
		w.written.Add(int64(end - start))
	}
	for start := 0; start < len(attempts); start += w.batchSize() {
		end := start + w.batchSize()
		if end > len(attempts) {
			end = len(attempts)
		}
		if err := w.insertAttempts(ctx, attempts[start:end]); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if w.Logger != nil {
				w.Logger.Warn(ctx, "upstream_attempt_insert_failed: "+err.Error(), obslog.Fields{Stage: "P29"})
			}
			continue
		}
		w.written.Add(int64(end - start))
	}
	return firstErr
}

func (w *UsageWriter) drainUsage() []UsageRecord {
	var out []UsageRecord
	for {
		select {
		case r := <-w.usageCh:
			out = append(out, r)
		default:
			return out
		}
	}
}

func (w *UsageWriter) drainAttempts() []AttemptRecord {
	var out []AttemptRecord
	for {
		select {
		case r := <-w.attemptCh:
			out = append(out, r)
		default:
			return out
		}
	}
}

const usageColumns = `request_id, user_id, api_key_id, api_key_name_snap,
model_id, model_name_snap, request_model_req, inbound_protocol,
channel_id, channel_name_snap, outbound_protocol, route_id, price_rule_id,
config_version, group_code_snap, price_ratio_bp, billing_tier,
cache_hit_tokens, cache_write_tokens, is_stream,
http_status, biz_code, result, fail_stage, fail_owner,
tokens_in, tokens_out, tokens_in_est, tokens_out_est, usage_source,
image_count, prehold_micro, consumed_micro, consumed_recharge_micro,
consumed_bonus_micro, refunded_micro, upstream_attempts, ttft_ms, total_ms,
upstream_ms, client_ip, user_agent, client_aborted, full_log_saved,
volume_basis, created_at`

const usageColumnCount = 46

func (w *UsageWriter) insertUsage(ctx context.Context, rows []UsageRecord) error {
	if len(rows) == 0 {
		return nil
	}
	args := make([]any, 0, len(rows)*usageColumnCount)
	for _, r := range rows {
		created := r.CreatedAt
		if created.IsZero() {
			created = w.now()
		}
		args = append(args,
			cut(r.RequestID, 36), r.UserID, r.APIKeyID, cut(r.APIKeyName, 64),
			nullID(r.ModelID), cut(r.ModelName, 128), cut(r.RequestModel, 128), cut(r.InboundProto, 24),
			nullID(r.ChannelID), nullableString(cut(r.ChannelName, 64)),
			nullableString(cut(r.OutboundProto, 24)), nullID(r.RouteID), nullID(r.PriceRuleID),
			r.ConfigVersion, firstNonEmpty(cut(r.GroupCode, 24), "standard"),
			clampInt(r.PriceRatioBP, 0, 65535), firstNonEmpty(cut(r.BillingTier, 16), "BASE"),
			0, 0, boolInt(r.IsStream),
			maxInt(r.HTTPStatus, 200), cut(r.BizCode, 32), firstNonEmpty(cut(r.Result, 16), "FAILED"),
			nullableString(cut(r.FailStage, 24)), nullableString(cut(r.FailOwner, 16)),
			clampInt(r.TokensIn, 0, 4294967295), clampInt(r.TokensOut, 0, 4294967295),
			clampInt(r.TokensInEst, 0, 4294967295), clampInt(r.TokensOutEst, 0, 4294967295),
			firstNonEmpty(cut(r.UsageSource, 16), "NONE"),
			clampInt(r.ImageCount, 0, 255), r.PreholdMicro, r.ConsumedMicro,
			r.ConsumedRechargeMicro, r.ConsumedBonusMicro, r.RefundedMicro,
			clampInt(r.UpstreamAttempts, 0, 255), r.TTFTMS,
			clampInt(r.TotalMS, 0, 4294967295), clampInt(r.UpstreamMS, 0, 4294967295),
			nullBytes(r.ClientIP), nullableString(cut(r.UserAgent, 255)),
			boolInt(r.ClientAborted), 0, "meta", created.UTC(),
		)
	}
	q := "INSERT INTO t_usage_record (" + usageColumns + ") VALUES " +
		placeholders(len(rows), usageColumnCount) +
		" ON DUPLICATE KEY UPDATE request_id = VALUES(request_id)"
	_, err := w.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("settle: insert usage rows: %w", err)
	}
	return nil
}

const attemptColumns = `request_id, attempt_index, user_id, model_id, route_id, channel_id,
channel_name_snap, protocol_code, upstream_model, phase, result,
http_status, upstream_code, upstream_msg, is_retryable, retry_decision,
connect_ms, first_byte_ms, total_ms, tokens_in, tokens_out, bytes_in, bytes_out,
error_class, created_at`

const attemptColumnCount = 25

func (w *UsageWriter) insertAttempts(ctx context.Context, rows []AttemptRecord) error {
	if len(rows) == 0 {
		return nil
	}
	args := make([]any, 0, len(rows)*attemptColumnCount)
	for _, r := range rows {
		created := r.CreatedAt
		if created.IsZero() {
			created = w.now()
		}
		args = append(args,
			cut(r.RequestID, 36), clampInt(int64(r.AttemptIndex), 0, 255), r.UserID, r.ModelID,
			r.RouteID, r.ChannelID, cut(r.ChannelName, 64), cut(r.ProtocolCode, 32),
			cut(r.UpstreamModel, 128), cut(r.Phase, 16), firstNonEmpty(cut(r.Result, 16), "FAILED"),
			nullPositive(r.HTTPStatus), nullableString(cut(r.UpstreamCode, 64)),
			nullableString(cut(r.UpstreamMsg, 512)), boolInt(r.IsRetryable),
			nullableString(cut(r.RetryDecision, 24)), nullPositive64(r.ConnectMS),
			nullPositive64(r.FirstByteMS), clampInt(r.TotalMS, 0, 4294967295),
			clampInt(r.TokensIn, 0, 4294967295), clampInt(r.TokensOut, 0, 4294967295),
			clampInt(r.BytesIn, 0, 4294967295), clampInt(r.BytesOut, 0, 4294967295),
			nullableString(cut(r.ErrorClass, 24)), created.UTC(),
		)
	}
	q := "INSERT INTO t_upstream_attempt (" + attemptColumns + ") VALUES " +
		placeholders(len(rows), attemptColumnCount) +
		" ON DUPLICATE KEY UPDATE request_id = VALUES(request_id)"
	_, err := w.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("settle: insert attempt rows: %w", err)
	}
	return nil
}

func (w *UsageWriter) batchSize() int {
	if w.BatchSize <= 0 {
		return DefaultFlushBatch
	}
	return w.BatchSize
}

func (w *UsageWriter) drop(kind string) {
	n := w.dropped.Add(1)
	if w.Logger != nil && n%100 == 1 {
		w.Logger.Warn(context.TODO(), fmt.Sprintf("usage_queue_full kind=%s dropped=%d", kind, n),
			obslog.Fields{Stage: "P28"})
	}
}

func (w *UsageWriter) now() time.Time {
	if w.Clock == nil {
		return time.Now().UTC()
	}
	return w.Clock.Now().UTC()
}

func placeholders(rows, cols int) string {
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", cols), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(one+",", rows), ",")
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullPositive(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullPositive64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func firstNonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func maxInt(v, floor int) int {
	if v < floor {
		return floor
	}
	return v
}

func clampInt(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
