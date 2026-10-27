package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"yunshu.dev/core/internal/billing"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// Rollup builds the T+1 metric tables:
//
//	t_metric_minute  -> t_metric_daily
//	t_usage_record   -> t_metric_model_daily
//	t_upstream_attempt -> t_metric_channel_health
//
// All writes are upserts keyed by the table's unique key, so re-running a day
// converges instead of duplicating.
type Rollup struct {
	DB       *sql.DB
	Logger   *obslog.Logger
	Clock    clock.Clock
	Location *time.Location
}

// RollupResult is one run.
type RollupResult struct {
	StatDate    string
	DailyRows   int64
	ModelRows   int64
	ChannelRows int64
}

// NewRollup builds the task.
func NewRollup(db *sql.DB, logger *obslog.Logger, clk clock.Clock) *Rollup {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Rollup{DB: db, Logger: logger, Clock: clk}
}

// Run rolls up one CST natural day. jobsd passes the previous day at 00:20.
func (r *Rollup) Run(ctx context.Context, day time.Time) (RollupResult, error) {
	var out RollupResult
	if r == nil || r.DB == nil {
		return out, fmt.Errorf("jobs: rollup requires a database")
	}
	loc := r.Location
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	local := day.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	out.StatDate = start.Format("2006-01-02")

	var err error
	out.DailyRows, err = r.rollupDaily(ctx, out.StatDate, start, end)
	if err != nil {
		return out, err
	}
	out.ModelRows, err = r.rollupModelDaily(ctx, out.StatDate, start, end)
	if err != nil {
		return out, err
	}
	out.ChannelRows, err = r.rollupChannelHealth(ctx, start, end)
	if err != nil {
		return out, err
	}
	if r.Logger != nil {
		r.Logger.Info(ctx, fmt.Sprintf("rollup done date=%s daily=%d model=%d channel=%d",
			out.StatDate, out.DailyRows, out.ModelRows, out.ChannelRows), obslog.Fields{Stage: "rollup"})
	}
	return out, nil
}

func (r *Rollup) rollupDaily(ctx context.Context, statDate string, start, end time.Time) (int64, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO t_metric_daily
		   (stat_date, scope_type, scope_id, request_total, request_success,
		    request_failed, request_biz_reject, consumed_micro,
		    consumed_recharge_micro, consumed_bonus_micro, tokens_in, tokens_out,
		    active_user_count, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms)
		 SELECT ?, scope_type, scope_id,
		        SUM(request_total), SUM(request_success), SUM(request_failed),
		        SUM(request_biz_reject), SUM(consumed_micro), 0, 0,
		        SUM(tokens_in), SUM(tokens_out), 0, NULL, NULL, NULL
		   FROM t_metric_minute
		  WHERE bucket_ts >= ? AND bucket_ts < ?
		  GROUP BY scope_type, scope_id
		 ON DUPLICATE KEY UPDATE
		   request_total = VALUES(request_total),
		   request_success = VALUES(request_success),
		   request_failed = VALUES(request_failed),
		   request_biz_reject = VALUES(request_biz_reject),
		   consumed_micro = VALUES(consumed_micro),
		   tokens_in = VALUES(tokens_in),
		   tokens_out = VALUES(tokens_out)`,
		statDate, start.UTC(), end.UTC())
	if err != nil {
		return 0, fmt.Errorf("jobs: rollup daily from minute: %w", err)
	}
	rows, _ := res.RowsAffected()

	for _, scope := range []struct{ typ, col string }{
		{"USER", "user_id"},
		{"MODEL", "model_id"},
		{"CHANNEL", "channel_id"},
	} {
		if err := r.fillUsageScope(ctx, statDate, scope.typ, scope.col, start, end); err != nil {
			return 0, err
		}
	}
	if err := r.fillGlobal(ctx, statDate, start, end); err != nil {
		return 0, err
	}
	for _, scope := range []struct{ typ, col string }{
		{"USER", "user_id"},
		{"MODEL", "model_id"},
		{"CHANNEL", "channel_id"},
		{"GLOBAL", "0"},
	} {
		if err := r.fillPercentiles(ctx, statDate, scope.typ, scope.col, start, end); err != nil {
			return 0, err
		}
	}
	return rows, nil
}

// fillUsageScope updates the recharge/bonus split and active users from the
// request log. The minute table is the counter source; it has no split.
func (r *Rollup) fillUsageScope(ctx context.Context, statDate, scopeType, column string, start, end time.Time) error {
	q := fmt.Sprintf(
		`UPDATE t_metric_daily d
		   JOIN (
		     SELECT %s AS sid,
		            SUM(consumed_recharge_micro) AS r,
		            SUM(consumed_bonus_micro) AS b,
		            COUNT(DISTINCT user_id) AS users
		       FROM t_usage_record
		      WHERE created_at >= ? AND created_at < ?
		      GROUP BY %s
		   ) u ON u.sid = d.scope_id
		    SET d.consumed_recharge_micro = u.r,
		        d.consumed_bonus_micro = u.b,
		        d.active_user_count = IF(? = 'USER', 1, u.users)
		  WHERE d.stat_date = ? AND d.scope_type = ?`,
		column, column)
	if _, err := r.DB.ExecContext(ctx, q, start.UTC(), end.UTC(), scopeType, statDate, scopeType); err != nil {
		return fmt.Errorf("jobs: rollup %s usage split: %w", scopeType, err)
	}
	return nil
}

func (r *Rollup) fillGlobal(ctx context.Context, statDate string, start, end time.Time) error {
	_, err := r.DB.ExecContext(ctx,
		`UPDATE t_metric_daily d
		   CROSS JOIN (
		     SELECT SUM(consumed_recharge_micro) AS r,
		            SUM(consumed_bonus_micro) AS b,
		            COUNT(DISTINCT user_id) AS users
		       FROM t_usage_record
		      WHERE created_at >= ? AND created_at < ?
		   ) u
		    SET d.consumed_recharge_micro = COALESCE(u.r, 0),
		        d.consumed_bonus_micro = COALESCE(u.b, 0),
		        d.active_user_count = COALESCE(u.users, 0)
		  WHERE d.stat_date = ? AND d.scope_type = 'GLOBAL'`,
		start.UTC(), end.UTC(), statDate)
	if err != nil {
		return fmt.Errorf("jobs: rollup GLOBAL usage split: %w", err)
	}
	return nil
}

func (r *Rollup) fillPercentiles(ctx context.Context, statDate, scopeType, column string, start, end time.Time) error {
	// GLOBAL passes the literal 0 as the scope key. It must be aliased in a
	// subquery: MySQL 8 reads a bare integer in PARTITION BY as a legacy column
	// position and rejects it (error 3592).
	q := fmt.Sprintf(
		`UPDATE t_metric_daily d
		   JOIN (
		     SELECT scope_id,
		            MAX(CASE WHEN rn = CEIL(cnt * 0.50) THEN ttft_ms END) AS p50,
		            MAX(CASE WHEN rn = CEIL(cnt * 0.95) THEN ttft_ms END) AS p95,
		            MAX(CASE WHEN rn = CEIL(cnt * 0.99) THEN ttft_ms END) AS p99
		       FROM (
		         SELECT scope_id, ttft_ms,
		                ROW_NUMBER() OVER (PARTITION BY scope_id ORDER BY ttft_ms) AS rn,
		                COUNT(*) OVER (PARTITION BY scope_id) AS cnt
		           FROM (
		             SELECT %s AS scope_id, ttft_ms
		               FROM t_usage_record
		              WHERE created_at >= ? AND created_at < ?
		                AND ttft_ms IS NOT NULL
		           ) base
		       ) x
		      GROUP BY scope_id
		   ) p ON p.scope_id = d.scope_id
		    SET d.ttft_p50_ms = p.p50,
		        d.ttft_p95_ms = p.p95,
		        d.ttft_p99_ms = p.p99
		  WHERE d.stat_date = ? AND d.scope_type = ?`,
		column)
	if _, err := r.DB.ExecContext(ctx, q, start.UTC(), end.UTC(), statDate, scopeType); err != nil {
		return fmt.Errorf("jobs: rollup %s ttft: %w", scopeType, err)
	}
	return nil
}

type modelAgg struct {
	requests   int64
	consumed   int64
	cost       int64
	configured bool
	rows       int64
}

func (r *Rollup) rollupModelDaily(ctx context.Context, statDate string, start, end time.Time) (int64, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT u.model_id, u.tokens_in, u.tokens_out, u.consumed_micro,
		        p.cost_in_micro_per_k, p.cost_out_micro_per_k
		   FROM t_usage_record u
		   LEFT JOIN t_price_rule p ON p.id = u.price_rule_id
		  WHERE u.created_at >= ? AND u.created_at < ?
		    AND u.model_id IS NOT NULL
		    AND u.result = 'SUCCESS'`,
		start.UTC(), end.UTC())
	if err != nil {
		return 0, fmt.Errorf("jobs: rollup model daily: %w", err)
	}
	defer rows.Close()

	aggs := map[int64]*modelAgg{}
	for rows.Next() {
		var modelID, tokensIn, tokensOut, consumed int64
		var costIn, costOut sql.NullInt64
		if err := rows.Scan(&modelID, &tokensIn, &tokensOut, &consumed, &costIn, &costOut); err != nil {
			return 0, fmt.Errorf("jobs: rollup model scan: %w", err)
		}
		a := aggs[modelID]
		if a == nil {
			a = &modelAgg{configured: true}
			aggs[modelID] = a
		}
		a.requests++
		a.consumed += consumed
		if costIn.Valid && costOut.Valid {
			a.cost += billing.CostMicro(tokensIn, tokensOut, costIn.Int64, costOut.Int64)
		} else {
			a.configured = false
		}
	}

	var written int64
	for modelID, a := range aggs {
		cost := int64(0)
		configured := 0
		var gross any
		if a.configured {
			configured = 1
			cost = a.cost
			gross = a.consumed - a.cost
		}
		_, err := r.DB.ExecContext(ctx,
			`INSERT INTO t_metric_model_daily
			   (stat_date, model_id, request_total, consumed_micro,
			    cost_micro, cost_configured, gross_profit_micro)
			 VALUES (?,?,?,?,?,?,?)
			 ON DUPLICATE KEY UPDATE
			   request_total = VALUES(request_total),
			   consumed_micro = VALUES(consumed_micro),
			   cost_micro = VALUES(cost_micro),
			   cost_configured = VALUES(cost_configured),
			   gross_profit_micro = VALUES(gross_profit_micro)`,
			statDate, modelID, a.requests, a.consumed, cost, configured, gross)
		if err != nil {
			return 0, fmt.Errorf("jobs: upsert model daily %d: %w", modelID, err)
		}
		written++
	}
	return written, nil
}

func (r *Rollup) rollupChannelHealth(ctx context.Context, start, end time.Time) (int64, error) {
	// created_at is datetime(3) holding UTC. UNIX_TIMESTAMP() would interpret
	// the literal in the session time zone, shifting every bucket by the
	// server's offset; TIMESTAMPDIFF against a literal epoch is zone-free.
	rows, err := r.DB.QueryContext(ctx,
		`SELECT (TIMESTAMPDIFF(SECOND, '1970-01-01 00:00:00', created_at) DIV 60) AS min_epoch,
		        channel_id,
		        COUNT(*) AS attempt_total,
		        COALESCE(SUM(result = 'SUCCESS'), 0) AS attempt_success,
		        COALESCE(ROUND(AVG(connect_ms)), 0) AS avg_connect,
		        COALESCE(ROUND(AVG(first_byte_ms)), 0) AS avg_first_byte,
		        COALESCE(ROUND(AVG(total_ms)), 0) AS avg_total,
		        COALESCE(SUM(error_class = 'TIMEOUT'), 0) AS err_timeout,
		        COALESCE(SUM(error_class = 'HTTP_5XX'), 0) AS err_5xx,
		        COALESCE(SUM(error_class = 'HTTP_4XX'), 0) AS err_4xx,
		        COALESCE(SUM(error_class = 'MALFORMED_BODY'), 0) AS err_malformed
		   FROM t_upstream_attempt
		  WHERE created_at >= ? AND created_at < ?
		  GROUP BY min_epoch, channel_id`,
		start.UTC(), end.UTC())
	if err != nil {
		return 0, fmt.Errorf("jobs: rollup channel health: %w", err)
	}
	defer rows.Close()

	type channelRow struct {
		epoch                          int64
		channelID                      int64
		total, success                 int64
		connect, firstByte, totalMs    int64
		timeout, e5xx, e4xx, malformed int64
	}
	var out []channelRow
	for rows.Next() {
		var c channelRow
		if err := rows.Scan(&c.epoch, &c.channelID, &c.total, &c.success,
			&c.connect, &c.firstByte, &c.totalMs, &c.timeout, &c.e5xx, &c.e4xx, &c.malformed); err != nil {
			return 0, fmt.Errorf("jobs: rollup channel scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("jobs: rollup channel rows: %w", err)
	}

	var written int64
	for _, c := range out {
		bucket := time.Unix(c.epoch*60, 0).UTC()
		_, err := r.DB.ExecContext(ctx,
			`INSERT INTO t_metric_channel_health
			   (bucket_ts, channel_id, attempt_total, attempt_success,
			    avg_connect_ms, avg_first_byte_ms, avg_total_ms,
			    error_timeout, error_5xx, error_4xx, error_malformed)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?)
			 ON DUPLICATE KEY UPDATE
			   attempt_total = VALUES(attempt_total),
			   attempt_success = VALUES(attempt_success),
			   avg_connect_ms = VALUES(avg_connect_ms),
			   avg_first_byte_ms = VALUES(avg_first_byte_ms),
			   avg_total_ms = VALUES(avg_total_ms),
			   error_timeout = VALUES(error_timeout),
			   error_5xx = VALUES(error_5xx),
			   error_4xx = VALUES(error_4xx),
			   error_malformed = VALUES(error_malformed)`,
			bucket, c.channelID, c.total, c.success,
			safeUint(c.connect), safeUint(c.firstByte), safeUint(c.totalMs),
			safeUint(c.timeout), safeUint(c.e5xx), safeUint(c.e4xx), safeUint(c.malformed))
		if err != nil {
			return 0, fmt.Errorf("jobs: upsert channel health: %w", err)
		}
		written++
	}
	return written, nil
}

// safeUint clamps a negative aggregate to zero; the target columns are
// unsigned ints and a negative AVG can only come from corrupt source rows.
func safeUint(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return v
}
