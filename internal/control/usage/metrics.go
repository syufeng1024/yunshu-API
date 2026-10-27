package usage

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/redisx"
)

// metricsWindow describes the realtime side's coverage so a caller can see
// the delay instead of assuming the number is live.
type metricsWindow struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Minutes    int       `json:"minutes"`
	LagSeconds int       `json:"lag_seconds"`
}

// metricsResponse keeps the two sources apart on purpose (T14 hard
// constraint 8). Every realtime value is prefixed realtime_, every rolled-up
// value settled_; there is no field that adds them. A chart that wants one
// line must pick a source explicitly.
type metricsResponse struct {
	Scope          string        `json:"scope"`
	ScopeID        int64         `json:"scope_id"`
	RealtimeScope  string        `json:"realtime_scope"`
	RealtimeSource string        `json:"realtime_source"`
	RealtimeWindow metricsWindow `json:"realtime_window"`

	RealtimeRequestTotal   int64 `json:"realtime_request_total"`
	RealtimeRequestSuccess int64 `json:"realtime_request_success"`
	RealtimeRequestFailed  int64 `json:"realtime_request_failed"`
	RealtimeTokensIn       int64 `json:"realtime_tokens_in"`
	RealtimeTokensOut      int64 `json:"realtime_tokens_out"`
	RealtimeConsumedMicro  int64 `json:"realtime_consumed_micro"`
	RealtimeConsumedFen    int64 `json:"realtime_consumed_fen"`

	SettledSource           string `json:"settled_source"`
	SettledFrom             string `json:"settled_from"`
	SettledUntil            string `json:"settled_until"`
	SettledRequestTotal     int64  `json:"settled_request_total"`
	SettledRequestSuccess   int64  `json:"settled_request_success"`
	SettledRequestFailed    int64  `json:"settled_request_failed"`
	SettledTokensIn         int64  `json:"settled_tokens_in"`
	SettledTokensOut        int64  `json:"settled_tokens_out"`
	SettledConsumedMicro    int64  `json:"settled_consumed_micro"`
	SettledConsumedRecharge int64  `json:"settled_consumed_recharge_micro"`
	SettledConsumedBonus    int64  `json:"settled_consumed_bonus_micro"`
	SettledConsumedFen      int64  `json:"settled_consumed_fen"`
	SettledSuccessRateBP    int64  `json:"settled_success_rate_bp"`
}

type realtimeTotals struct {
	requests, success, failed int64
	tokensIn, tokensOut       int64
	consumedMicro             int64
}

// Metrics implements GET /api/console/usage/metrics.
//
// scope=user (default) reads K14 m:USER:{uid}:{minute}; scope=channel requires
// channel_id and reads K15 m:chan:{id}:{minute}, which is platform-wide — the
// response names that explicitly as realtime_scope=PLATFORM. History always
// reads t_metric_daily, whose stat_date is a CST natural day, and stops at
// yesterday because today's row is only written by the T+1 rollup.
func (s *Service) Metrics(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := r.URL.Query()

	scope := strings.ToLower(strings.TrimSpace(q.Get("scope")))
	if scope == "" {
		scope = "user"
	}
	days := 7
	if raw := strings.TrimSpace(q.Get("days")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 365 {
			mw.WriteError(w, r, mw.InvalidQueryField("days"))
			return
		}
		days = v
	}

	var (
		scopeType   string
		scopeID     int64
		keyFor      func(minEpoch int64) string
		realtimeTag string
		realtimeSrc string
	)
	switch scope {
	case "user":
		scopeType, scopeID = "USER", uid
		realtimeTag, realtimeSrc = "ACCOUNT", "REDIS_K14"
		keyFor = func(minEpoch int64) string {
			return redisx.MetricKey("USER", uid, minEpoch)
		}
	case "channel":
		raw := strings.TrimSpace(q.Get("channel_id"))
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			mw.WriteError(w, r, mw.InvalidQueryField("channel_id"))
			return
		}
		scopeType, scopeID = "CHANNEL", v
		realtimeTag, realtimeSrc = "PLATFORM", "REDIS_K15"
		keyFor = func(minEpoch int64) string {
			return redisx.ChannelMetricKey(v, minEpoch)
		}
	default:
		mw.WriteError(w, r, mw.InvalidQueryField("scope"))
		return
	}

	rt, apiErr := s.readRealtime(r.Context(), keyFor)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	now := s.now()
	until := lastCompleteCSTDay(now)
	from := until.AddDate(0, 0, -(days - 1))
	settled, apiErr := s.readSettled(r.Context(), scopeType, scopeID, from, until)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	_ = mw.WriteJSON(w, http.StatusOK, metricsResponse{
		Scope:          scope,
		ScopeID:        scopeID,
		RealtimeScope:  realtimeTag,
		RealtimeSource: realtimeSrc,
		RealtimeWindow: metricsWindow{
			From:       now.Add(-time.Duration(realtimeBuckets-1) * time.Minute).UTC(),
			To:         now.UTC(),
			Minutes:    realtimeBuckets,
			LagSeconds: realtimeLagSeconds,
		},
		RealtimeRequestTotal:   rt.requests,
		RealtimeRequestSuccess: rt.success,
		RealtimeRequestFailed:  rt.failed,
		RealtimeTokensIn:       rt.tokensIn,
		RealtimeTokensOut:      rt.tokensOut,
		RealtimeConsumedMicro:  rt.consumedMicro,
		RealtimeConsumedFen:    fenFromMicro(rt.consumedMicro),

		SettledSource:           "T_METRIC_DAILY",
		SettledFrom:             from.Format("2006-01-02"),
		SettledUntil:            until.Format("2006-01-02"),
		SettledRequestTotal:     settled.requestTotal,
		SettledRequestSuccess:   settled.success,
		SettledRequestFailed:    settled.failed,
		SettledTokensIn:         settled.tokensIn,
		SettledTokensOut:        settled.tokensOut,
		SettledConsumedMicro:    settled.consumedMicro,
		SettledConsumedRecharge: settled.rechargeMicro,
		SettledConsumedBonus:    settled.bonusMicro,
		SettledConsumedFen:      fenFromMicro(settled.consumedMicro),
		SettledSuccessRateBP:    successRateBP(settled.success, settled.requestTotal),
	})
}

// readRealtime sums the last 60 one-minute hashes. Missing buckets (expired
// keys, no traffic) contribute zero; the pipeline still costs one round trip.
func (s *Service) readRealtime(ctx context.Context, keyFor func(int64) string) (realtimeTotals, *apierr.APIError) {
	var out realtimeTotals
	if s.Redis == nil {
		return out, apierr.New(apierr.CodeInternalError)
	}
	current := s.now().Unix() / 60
	cmdCtx, cancel := s.Redis.CommandContext(ctx)
	defer cancel()

	pipe := s.Redis.Raw().Pipeline()
	cmds := make([]*goredis.MapStringStringCmd, realtimeBuckets)
	for i := 0; i < realtimeBuckets; i++ {
		cmds[i] = pipe.HGetAll(cmdCtx, keyFor(current-int64(i)))
	}
	if _, err := pipe.Exec(cmdCtx); err != nil && !errors.Is(err, goredis.Nil) {
		s.logError(ctx, "usage_realtime_read_failed: "+err.Error())
		return out, apierr.New(apierr.CodeInternalError)
	}
	for _, cmd := range cmds {
		m, err := cmd.Result()
		if err != nil {
			continue
		}
		out.requests += hashInt(m, "req")
		out.success += hashInt(m, "ok")
		out.failed += hashInt(m, "fail")
		out.tokensIn += hashInt(m, "tok_in")
		out.tokensOut += hashInt(m, "tok_out")
		out.consumedMicro += hashInt(m, "consumed_micro")
	}
	return out, nil
}

type settledTotals struct {
	requestTotal, success, failed int64
	tokensIn, tokensOut           int64
	consumedMicro                 int64
	rechargeMicro, bonusMicro     int64
}

func (s *Service) readSettled(ctx context.Context, scopeType string, scopeID int64, from, until time.Time) (settledTotals, *apierr.APIError) {
	var out settledTotals
	err := s.DB.QueryRowContext(ctx, `
		SELECT CAST(COALESCE(SUM(request_total), 0) AS SIGNED),
		       CAST(COALESCE(SUM(request_success), 0) AS SIGNED),
		       CAST(COALESCE(SUM(request_failed), 0) AS SIGNED),
		       CAST(COALESCE(SUM(tokens_in), 0) AS SIGNED),
		       CAST(COALESCE(SUM(tokens_out), 0) AS SIGNED),
		       CAST(COALESCE(SUM(consumed_micro), 0) AS SIGNED),
		       CAST(COALESCE(SUM(consumed_recharge_micro), 0) AS SIGNED),
		       CAST(COALESCE(SUM(consumed_bonus_micro), 0) AS SIGNED)
		  FROM t_metric_daily
		 WHERE scope_type = ? AND scope_id = ?
		   AND stat_date >= ? AND stat_date <= ?`,
		scopeType, scopeID, from.Format("2006-01-02"), until.Format("2006-01-02"),
	).Scan(&out.requestTotal, &out.success, &out.failed,
		&out.tokensIn, &out.tokensOut, &out.consumedMicro,
		&out.rechargeMicro, &out.bonusMicro)
	if err != nil {
		s.logError(ctx, "usage_settled_read_failed: "+err.Error())
		return out, apierr.New(apierr.CodeInternalError)
	}
	return out, nil
}

// lastCompleteCSTDay returns 00:00 CST of yesterday: the newest stat_date the
// T+1 rollup can have written.
func lastCompleteCSTDay(now time.Time) time.Time {
	local := now.In(cst)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cst)
	return today.AddDate(0, 0, -1)
}

func hashInt(m map[string]string, field string) int64 {
	v, err := strconv.ParseInt(m[field], 10, 64)
	if err != nil {
		return 0
	}
	return v
}
