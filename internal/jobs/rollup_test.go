package jobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

// Rollup walks minute -> daily -> model-daily -> channel-health for one CST
// day. Every write is an upsert, so a second run converges.
func TestRollupBuildsAllFourTables(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 6, 10, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	statDate := day.Format("2006-01-02")
	start := day.UTC()
	end := start.Add(24 * time.Hour)
	bucket := start.Add(10 * time.Hour)
	const (
		user    = int64(950001)
		model   = int64(950002)
		channel = int64(950003)
		rule    = int64(950004)
	)

	cleanupRollup(t, statDate, start, end, user, model, channel, rule)
	t.Cleanup(func() { cleanupRollup(t, statDate, start, end, user, model, channel, rule) })

	now := time.Now().UTC()
	if _, err := testDB.Exec(
		`INSERT INTO t_price_rule
		   (id, scope_level, scope_id, model_type, in_price_micro_per_k,
		    out_price_micro_per_k, cost_in_micro_per_k, cost_out_micro_per_k,
		    effective_from, enabled, created_by, created_at)
		 VALUES (?, 'MODEL', ?, 'TEXT', 1000, 2000, 500, 1000, ?, 1, 1, ?)`,
		rule, model, start.AddDate(0, 0, -1), now); err != nil {
		t.Fatalf("insert price rule: %v", err)
	}

	for _, scope := range []struct {
		typ string
		id  int64
	}{{"USER", user}, {"MODEL", model}, {"CHANNEL", channel}, {"GLOBAL", 0}} {
		if _, err := testDB.Exec(
			`INSERT INTO t_metric_minute
			   (bucket_ts, scope_type, scope_id, request_total, request_success,
			    request_failed, request_biz_reject, consumed_micro, tokens_in, tokens_out,
			    ttft_sum_ms, ttft_count, total_ms_sum)
			 VALUES (?,?,?,2,1,1,0,700,1000,500,200,2,200)`,
			bucket, scope.typ, scope.id); err != nil {
			t.Fatalf("insert minute %s: %v", scope.typ, err)
		}
	}

	if _, err := testDB.Exec(
		`INSERT INTO t_usage_record
		   (request_id, user_id, api_key_id, api_key_name_snap, model_id, model_name_snap,
		    request_model_req, inbound_protocol, channel_id, price_rule_id, http_status,
		    result, tokens_in, tokens_out, consumed_micro, consumed_recharge_micro,
		    consumed_bonus_micro, ttft_ms, total_ms, created_at)
		 VALUES (?,?,1,'rollup',?,'fixture-model','fixture-model','OPENAI_COMPAT',?,
		         ?,200,'SUCCESS',1000,500,700,500,200,100,500,?)`,
		jobRID("rllu", 1), user, model, channel, rule, bucket.Add(5*time.Minute)); err != nil {
		t.Fatalf("insert usage: %v", err)
	}

	for _, a := range []struct {
		index   int
		result  string
		class   any
		totalMs int
	}{{1, "SUCCESS", nil, 100}, {2, "FAILED", "TIMEOUT", 300}} {
		if _, err := testDB.Exec(
			`INSERT INTO t_upstream_attempt
			   (request_id, attempt_index, user_id, model_id, route_id, channel_id,
			    channel_name_snap, protocol_code, upstream_model, phase, result,
			    error_class, total_ms, created_at)
			 VALUES (?,?,?,?,1,?,'fixture','OPENAI_COMPAT','fixture','COMPLETE',?,?,?,?)`,
			jobRID("rllu", 1), a.index, user, model, channel, a.result, a.class, a.totalMs,
			bucket.Add(time.Duration(a.index)*10*time.Second)); err != nil {
			t.Fatalf("insert attempt: %v", err)
		}
	}

	task := NewRollup(testDB, nil, clock.Real{})
	task.Location = day.Location()
	res, err := task.Run(ctx, day)
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if res.StatDate != statDate || res.ChannelRows != 1 {
		t.Fatalf("rollup result = %+v; want date=%s channel_rows=1", res, statDate)
	}

	for _, scope := range []struct {
		typ string
		id  int64
	}{{"USER", user}, {"MODEL", model}, {"CHANNEL", channel}, {"GLOBAL", 0}} {
		var total, consumed, recharge, bonus, active int64
		var p50 sql.NullInt64
		if err := testDB.QueryRow(
			`SELECT request_total, consumed_micro, consumed_recharge_micro,
			        consumed_bonus_micro, active_user_count, ttft_p50_ms
			   FROM t_metric_daily
			  WHERE stat_date = ? AND scope_type = ? AND scope_id = ?`,
			statDate, scope.typ, scope.id,
		).Scan(&total, &consumed, &recharge, &bonus, &active, &p50); err != nil {
			t.Fatalf("read daily %s: %v", scope.typ, err)
		}
		if total != 2 || consumed != 700 || recharge != 500 || bonus != 200 || active != 1 {
			t.Fatalf("daily %s = total=%d consumed=%d split=%d/%d active=%d; want 2/700/500/200/1",
				scope.typ, total, consumed, recharge, bonus, active)
		}
		if !p50.Valid || p50.Int64 != 100 {
			t.Fatalf("daily %s p50 = %v; want 100", scope.typ, p50)
		}
	}

	var requests, consumed, cost, configured, gross int64
	if err := testDB.QueryRow(
		`SELECT request_total, consumed_micro, cost_micro, cost_configured, gross_profit_micro
		   FROM t_metric_model_daily WHERE stat_date = ? AND model_id = ?`,
		statDate, model,
	).Scan(&requests, &consumed, &cost, &configured, &gross); err != nil {
		t.Fatalf("read model daily: %v", err)
	}
	// (1000*500 + 500*1000 + 999) / 1000 = 1000; gross = 700 - 1000.
	if requests != 1 || consumed != 700 || cost != 1000 || configured != 1 || gross != -300 {
		t.Fatalf("model daily = req=%d consumed=%d cost=%d configured=%d gross=%d; want 1/700/1000/1/-300",
			requests, consumed, cost, configured, gross)
	}

	var attemptTotal, success, avgTotal, timeouts int64
	if err := testDB.QueryRow(
		`SELECT attempt_total, attempt_success, avg_total_ms, error_timeout
		   FROM t_metric_channel_health
		  WHERE channel_id = ? AND bucket_ts = ?`,
		channel, bucket,
	).Scan(&attemptTotal, &success, &avgTotal, &timeouts); err != nil {
		t.Fatalf("read channel health: %v", err)
	}
	if attemptTotal != 2 || success != 1 || avgTotal != 200 || timeouts != 1 {
		t.Fatalf("channel health = total=%d success=%d avg=%d timeout=%d; want 2/1/200/1",
			attemptTotal, success, avgTotal, timeouts)
	}

	// Idempotent rerun: no duplicate rows, identical values.
	var dailyRows, channelRows int
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM t_metric_daily WHERE stat_date = ?`, statDate).Scan(&dailyRows); err != nil {
		t.Fatalf("count daily: %v", err)
	}
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM t_metric_channel_health WHERE channel_id = ? AND bucket_ts >= ? AND bucket_ts < ?`,
		channel, start, end).Scan(&channelRows); err != nil {
		t.Fatalf("count channel: %v", err)
	}
	if _, err := task.Run(ctx, day); err != nil {
		t.Fatalf("reroll: %v", err)
	}
	var dailyAfter, channelAfter int
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM t_metric_daily WHERE stat_date = ?`, statDate).Scan(&dailyAfter); err != nil {
		t.Fatalf("count daily after: %v", err)
	}
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM t_metric_channel_health WHERE channel_id = ? AND bucket_ts >= ? AND bucket_ts < ?`,
		channel, start, end).Scan(&channelAfter); err != nil {
		t.Fatalf("count channel after: %v", err)
	}
	if dailyRows != dailyAfter || channelRows != channelAfter {
		t.Fatalf("rerun duplicated rows: daily %d->%d channel %d->%d",
			dailyRows, dailyAfter, channelRows, channelAfter)
	}
	t.Logf("rollup daily_rows=%d model(req=%d cost=%d gross=%d) channel(total=%d success=%d avg=%d timeout=%d)",
		dailyRows, requests, cost, gross, attemptTotal, success, avgTotal, timeouts)
}

func cleanupRollup(t *testing.T, statDate string, start, end time.Time, user, model, channel, rule int64) {
	t.Helper()
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM t_metric_channel_health WHERE channel_id = ? AND bucket_ts >= ? AND bucket_ts < ?`,
			[]any{channel, start, end}},
		{`DELETE FROM t_metric_model_daily WHERE model_id = ?`, []any{model}},
		{`DELETE FROM t_metric_daily WHERE stat_date = ?`, []any{statDate}},
		{`DELETE FROM t_metric_minute WHERE bucket_ts >= ? AND bucket_ts < ?`, []any{start, end}},
		{`DELETE FROM t_upstream_attempt WHERE user_id = ?`, []any{user}},
		{`DELETE FROM t_usage_record WHERE user_id = ?`, []any{user}},
		{`DELETE FROM t_price_rule WHERE id = ?`, []any{rule}},
	}
	for _, s := range stmts {
		if _, err := testDB.Exec(s.q, s.args...); err != nil {
			t.Fatalf("rollup cleanup: %v\nquery: %s", err, s.q)
		}
	}
}
