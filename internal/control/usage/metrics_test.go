package usage

import (
	"context"
	"net/http"
	"testing"

	"yunshu.dev/core/internal/redisx"
)

func seedRealtime(t *testing.T, svc *Service, key string, fields map[string]any) {
	t.Helper()
	if err := svc.Redis.Raw().HSet(context.Background(), key, fields).Err(); err != nil {
		t.Fatalf("seed realtime %s: %v", key, err)
	}
}

func seedDaily(t *testing.T, statDate, scopeType string, scopeID int64,
	requestTotal, success, failed, consumed, recharge, bonus, tokensIn, tokensOut int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := testDB.ExecContext(ctx,
		`DELETE FROM t_metric_daily WHERE stat_date = ? AND scope_type = ? AND scope_id = ?`,
		statDate, scopeType, scopeID); err != nil {
		t.Fatalf("clear metric daily: %v", err)
	}
	_, err := testDB.ExecContext(ctx, `
		INSERT INTO t_metric_daily
			(stat_date, scope_type, scope_id, request_total, request_success,
			 request_failed, consumed_micro, consumed_recharge_micro,
			 consumed_bonus_micro, tokens_in, tokens_out)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		statDate, scopeType, scopeID, requestTotal, success, failed,
		consumed, recharge, bonus, tokensIn, tokensOut)
	if err != nil {
		t.Fatalf("seed metric daily: %v", err)
	}
}

// Hard constraint 8: realtime (K14/K15) and settled (t_metric_daily) are
// reported in separate field families with their own coverage windows. The
// test deliberately makes the two numbers different and asserts no combined
// total exists, so an implementation that adds them fails.
func TestMetricsSeparatesRealtimeAndSettled(t *testing.T) {
	uid := newTestUser(t)
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)

	epoch := testBase.Unix() / 60
	seedRealtime(t, svc, redisx.MetricKey("USER", uid, epoch), map[string]any{
		"req": 3, "ok": 2, "fail": 1, "tok_in": 10, "tok_out": 20,
		"consumed_micro": 1_000,
	})
	seedRealtime(t, svc, redisx.MetricKey("USER", uid, epoch-1), map[string]any{
		"req": 1, "ok": 1, "fail": 0, "tok_in": 5, "tok_out": 5,
		"consumed_micro": 2_000,
	})
	// Yesterday in CST relative to 2026-10-27T08:00Z is 2026-10-26.
	seedDaily(t, "2026-10-26", "USER", uid, 10, 9, 1,
		50_000_000, 40_000_000, 10_000_000, 100, 200)

	rec := callHandler(t, svc.Metrics, "/api/console/usage/metrics?days=7", uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out metricsResponse
	decodeBody(t, rec, &out)

	if out.RealtimeRequestTotal != 4 || out.RealtimeRequestSuccess != 3 || out.RealtimeRequestFailed != 1 {
		t.Fatalf("realtime requests=%d/%d/%d want 4/3/1",
			out.RealtimeRequestTotal, out.RealtimeRequestSuccess, out.RealtimeRequestFailed)
	}
	if out.RealtimeTokensIn != 15 || out.RealtimeTokensOut != 25 {
		t.Fatalf("realtime tokens=%d/%d want 15/25", out.RealtimeTokensIn, out.RealtimeTokensOut)
	}
	if out.RealtimeConsumedMicro != 3_000 {
		t.Fatalf("realtime consumed=%d want 3000", out.RealtimeConsumedMicro)
	}
	if out.RealtimeSource != "REDIS_K14" || out.RealtimeScope != "ACCOUNT" {
		t.Fatalf("realtime source=%q scope=%q", out.RealtimeSource, out.RealtimeScope)
	}
	if out.RealtimeWindow.Minutes != realtimeBuckets || out.RealtimeWindow.LagSeconds != realtimeLagSeconds {
		t.Fatalf("realtime window=%+v", out.RealtimeWindow)
	}

	if out.SettledRequestTotal != 10 || out.SettledRequestSuccess != 9 {
		t.Fatalf("settled requests=%d/%d want 10/9",
			out.SettledRequestTotal, out.SettledRequestSuccess)
	}
	if out.SettledConsumedMicro != 50_000_000 || out.SettledConsumedFen != 5_000 {
		t.Fatalf("settled consumed=%d micro / %d fen want 50000000/5000",
			out.SettledConsumedMicro, out.SettledConsumedFen)
	}
	if out.SettledConsumedRecharge+out.SettledConsumedBonus != out.SettledConsumedMicro {
		t.Fatalf("settled split %d+%d != %d", out.SettledConsumedRecharge,
			out.SettledConsumedBonus, out.SettledConsumedMicro)
	}
	if out.SettledUntil != "2026-10-26" || out.SettledFrom != "2026-10-20" {
		t.Fatalf("settled window=%s..%s want 2026-10-20..2026-10-26",
			out.SettledFrom, out.SettledUntil)
	}
	if out.RealtimeConsumedMicro == out.SettledConsumedMicro {
		t.Fatal("test setup is not distinguishing the two sources")
	}

	var raw map[string]any
	decodeBody(t, rec, &raw)
	for _, forbidden := range []string{"request_total", "consumed_micro", "total"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("response has a combined %q field; realtime and settled must not be summed", forbidden)
		}
	}
	t.Logf("realtime: req=%d ok=%d fail=%d tokens=%d/%d micro=%d window=%+v",
		out.RealtimeRequestTotal, out.RealtimeRequestSuccess, out.RealtimeRequestFailed,
		out.RealtimeTokensIn, out.RealtimeTokensOut, out.RealtimeConsumedMicro, out.RealtimeWindow)
	t.Logf("settled:  req=%d ok=%d fail=%d tokens=%d/%d micro=%d until=%s",
		out.SettledRequestTotal, out.SettledRequestSuccess, out.SettledRequestFailed,
		out.SettledTokensIn, out.SettledTokensOut, out.SettledConsumedMicro, out.SettledUntil)
}

// scope=channel reads K15 and labels the source PLATFORM, because K15 is not
// per-account. The label is part of the contract: the console must not show a
// platform-wide counter as the user's own traffic.
func TestMetricsChannelScopeReadsK15(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	cid := uid + 1_000_000
	epoch := testBase.Unix() / 60
	seedRealtime(t, svc, redisx.ChannelMetricKey(cid, epoch), map[string]any{
		"req": 5, "ok": 5, "fail": 0, "tok_in": 7, "tok_out": 8,
		"consumed_micro": 7_000,
	})

	rec := callHandler(t, svc.Metrics,
		"/api/console/usage/metrics?scope=channel&channel_id="+itoa(cid), uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out metricsResponse
	decodeBody(t, rec, &out)
	if out.RealtimeRequestTotal != 5 || out.RealtimeConsumedMicro != 7_000 {
		t.Fatalf("channel realtime=%d req / %d micro, want 5/7000",
			out.RealtimeRequestTotal, out.RealtimeConsumedMicro)
	}
	if out.RealtimeSource != "REDIS_K15" || out.RealtimeScope != "PLATFORM" {
		t.Fatalf("source=%q scope=%q want REDIS_K15/PLATFORM",
			out.RealtimeSource, out.RealtimeScope)
	}
	if out.SettledRequestTotal != 0 {
		t.Fatalf("settled=%d want 0 for a channel with no daily row", out.SettledRequestTotal)
	}
	t.Logf("channel K15: scope=%s source=%s realtime_req=%d realtime_micro=%d",
		out.RealtimeScope, out.RealtimeSource, out.RealtimeRequestTotal, out.RealtimeConsumedMicro)
}
