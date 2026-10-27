package recon

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	"yunshu.dev/core/internal/billing"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// CST is the product's statutory timezone. A fixed zone avoids depending on
// the host tzdata (the Windows test host does not ship Asia/Shanghai).
var CST = time.FixedZone("CST", 8*3600)

// Daily defaults.
const (
	DailyShardSize              int64 = 10_000
	DefaultIdemExpiredThreshold       = 100_000
	DailyLedgerPage                   = 1_000
	R05HighMicro                int64 = 100
	R05CriticalMicro            int64 = 10_000
)

// DayLocker is the K21 surface. *lock.Locker satisfies it.
type DayLocker interface {
	AcquireRecon(ctx context.Context, day, owner string) (bool, error)
}

// Daily is the read-only reconciliation task.
//
// 只读：本类型只写 t_reconcile_diff 与 t_notification。它没有任何修改余额、
// 流水、批次或额度计数器的代码路径（T09 硬约束 9）。
type Daily struct {
	DB        *sql.DB
	Locker    DayLocker
	Recorder  *Recorder
	Logger    *obslog.Logger
	Clock     clock.Clock
	Owner     string
	ShardSize int64
	// IdemThreshold is the R-07 trigger. It defaults to 100,000; tests lower it
	// instead of inserting 100,001 rows. Production never changes it.
	IdemThreshold int
	// Location is the business zone; nil means CST.
	Location *time.Location
	// UserLo/UserHi bound the internal checks to [UserLo, UserHi). Zero, zero
	// means the whole table, which is the only shape production uses. Tests
	// sharing a schema set them so another fixture's accounts cannot appear in
	// R-01..R-07.
	UserLo, UserHi int64
}

// shardBounds returns the user range the internal checks cover. The unbounded
// case keeps the historical full-table semantics.
func (d *Daily) shardBounds() (int64, int64) {
	if d.UserLo == 0 && d.UserHi == 0 {
		return 0, math.MaxInt64
	}
	lo, hi := d.UserLo, d.UserHi
	if lo < 0 {
		lo = 0
	}
	if hi <= lo {
		hi = math.MaxInt64
	}
	return lo, hi
}

// NewDaily builds the daily reconciler.
func NewDaily(db *sql.DB, locker DayLocker, logger *obslog.Logger, clk clock.Clock, owner string) *Daily {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Daily{
		DB:       db,
		Locker:   locker,
		Recorder: NewRecorder(db, logger, clk),
		Logger:   logger,
		Clock:    clk,
		Owner:    owner,
	}
}

// DailyResult is one run's summary.
type DailyResult struct {
	Skipped  bool
	BizDate  string
	Diffs    []Diff
	Internal int
	External int
	Recorded int
	Changed  int
	P0       bool
	P0Reason string
}

// Check is one of R-01..R-08, exposed for table-driven acceptance tests.
type Check struct {
	Code string
	Run  func(ctx context.Context, d *Daily, bizDate string, dayStart, dayEnd time.Time) ([]Diff, error)
}

// Checks returns R-01..R-08 in order.
func (d *Daily) Checks() []Check {
	return []Check{
		{DiffR01, checkR01},
		{DiffR02, checkR02},
		{DiffR03, checkR03},
		{DiffR04, checkR04},
		{DiffR05, checkR05},
		{DiffR06, checkR06},
		{DiffR07, checkR07},
		{DiffR08, checkR08},
	}
}

// Run reconciles one natural day (UTC+8).
func (d *Daily) Run(ctx context.Context, day time.Time) (DailyResult, error) {
	var out DailyResult
	if d == nil || d.DB == nil || d.Recorder == nil {
		return out, fmt.Errorf("recon: daily requires db and recorder")
	}
	loc := d.location()
	localDay := day.In(loc)
	dayStart := time.Date(localDay.Year(), localDay.Month(), localDay.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)
	out.BizDate = dayStart.Format("2006-01-02")

	if d.Locker != nil {
		ok, err := d.Locker.AcquireRecon(ctx, out.BizDate, d.Owner)
		if err != nil {
			return out, fmt.Errorf("recon: acquire K21 %s: %w", out.BizDate, err)
		}
		if !ok {
			out.Skipped = true
			return out, nil
		}
	}

	internal, err := d.runInternal(ctx, out.BizDate, dayStart, dayEnd)
	if err != nil {
		return out, err
	}
	external, p0, p0Reason, err := d.runExternal(ctx, dayStart, dayEnd)
	if err != nil {
		return out, err
	}

	out.Diffs = append(internal, external...)
	out.Internal = len(internal)
	out.External = len(external)
	out.P0 = p0
	out.P0Reason = p0Reason

	alerted := map[string]bool{}
	for _, diff := range out.Diffs {
		changed, err := d.Recorder.Record(ctx, diff)
		if err != nil {
			return out, err
		}
		out.Recorded++
		if changed {
			out.Changed++
		}
		d.Recorder.Alert(ctx, diff)
		if diff.Level == LevelCritical && !alerted[diff.DiffType] {
			alerted[diff.DiffType] = true
			body := fmt.Sprintf("biz_date=%s diff_type=%s subject=%s local=%d upstream=%d delta=%d",
				diff.BizDate, diff.DiffType, diff.Subject, diff.LocalValue, diff.UpstreamValue, diff.DeltaMicro)
			if err := d.Recorder.Notify(ctx, "RECONCILE_ALERT",
				"对账 CRITICAL："+diff.DiffType, body, 3); err != nil {
				return out, err
			}
		}
	}
	if p0 {
		body := fmt.Sprintf("biz_date=%s reason=%s diffs=%d", out.BizDate, p0Reason, len(external))
		if err := d.Recorder.Notify(ctx, "RECONCILE_P0",
			"对账 P0，15 分钟内响应", body, 7); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (d *Daily) runInternal(ctx context.Context, bizDate string, dayStart, dayEnd time.Time) ([]Diff, error) {
	var all []Diff
	for _, check := range d.Checks() {
		diffs, err := check.Run(ctx, d, bizDate, dayStart, dayEnd)
		if err != nil {
			return all, fmt.Errorf("recon: %s failed: %w", check.Code, err)
		}
		all = append(all, diffs...)
	}
	return all, nil
}

func (d *Daily) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return CST
}

func (d *Daily) now() time.Time {
	if d == nil || d.Clock == nil {
		return time.Now().UTC()
	}
	return d.Clock.Now().UTC()
}

func (d *Daily) shardSize() int64 {
	if d.ShardSize <= 0 {
		return DailyShardSize
	}
	return d.ShardSize
}

// ---- R-01 .. R-08 -------------------------------------------------------

// R-01: materialised balance == sum of ledger entries. No tolerance.
func checkR01(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	const q = `SELECT a.user_id, a.balance_micro,
	                  COALESCE(SUM(l.amount_total_micro), 0) AS ledger_sum
	             FROM t_credit_account a
	             LEFT JOIN t_ledger_entry l ON l.user_id = a.user_id
	            WHERE a.user_id >= ? AND a.user_id < ?
	            GROUP BY a.user_id, a.balance_micro
	           HAVING a.balance_micro <> ledger_sum`
	return d.scanDiffs(ctx, "t_credit_account", q,
		func(rows *sql.Rows) (Diff, error) {
			var userID, balance, sum int64
			if err := rows.Scan(&userID, &balance, &sum); err != nil {
				return Diff{}, err
			}
			return Diff{
				BizDate: bizDate, DiffType: DiffR01, Subject: strconv.FormatInt(userID, 10),
				LocalValue: balance, UpstreamValue: sum, DeltaMicro: balance - sum,
				MatchMode: MatchModeInternal, Level: LevelCritical,
			}, nil
		})
}

// R-02: balance_micro == recharge + bonus. No tolerance.
func checkR02(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	const q = `SELECT user_id, balance_micro,
	                  balance_recharge_micro + balance_bonus_micro AS two_accounts
	             FROM t_credit_account
	            WHERE user_id >= ? AND user_id < ?
	              AND balance_micro <> balance_recharge_micro + balance_bonus_micro`
	return d.scanDiffs(ctx, "t_credit_account", q,
		func(rows *sql.Rows) (Diff, error) {
			var userID, balance, sum int64
			if err := rows.Scan(&userID, &balance, &sum); err != nil {
				return Diff{}, err
			}
			return Diff{
				BizDate: bizDate, DiffType: DiffR02, Subject: strconv.FormatInt(userID, 10),
				LocalValue: balance, UpstreamValue: sum, DeltaMicro: balance - sum,
				MatchMode: MatchModeInternal, Level: LevelCritical,
			}, nil
		})
}

// R-03: each entry's balance_before equals the previous entry's
// balance_after, ordered by id within a user. Per-user pagination follows the
// TDS shard guidance; the subject is the earlier entry id.
func checkR03(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	var diffs []Diff
	lo, hi := d.shardBounds()
	maxUser, err := d.maxUser(ctx, "t_ledger_entry", "")
	if err != nil {
		return nil, err
	}
	for shardLo := lo; shardLo <= maxUser; shardLo += d.shardSize() {
		shardHi := shardLo + d.shardSize()
		if shardHi > hi {
			shardHi = hi
		}
		users, err := d.userIDs(ctx,
			`SELECT user_id FROM t_ledger_entry
			  WHERE user_id >= ? AND user_id < ? GROUP BY user_id`, shardLo, shardHi)
		if err != nil {
			return nil, err
		}
		for _, uid := range users {
			var lastID int64
			var prevID, prevAfter int64
			havePrev := false
			for {
				rows, err := d.DB.QueryContext(ctx,
					`SELECT id, request_id, balance_before_micro, balance_after_micro
					   FROM t_ledger_entry
					  WHERE user_id = ? AND id > ?
					  ORDER BY id ASC
					  LIMIT ?`, uid, lastID, DailyLedgerPage)
				if err != nil {
					return diffs, fmt.Errorf("recon: R-03 page user=%d: %w", uid, err)
				}
				n := 0
				for rows.Next() {
					var id, before, after int64
					var requestID sql.NullString
					if err := rows.Scan(&id, &requestID, &before, &after); err != nil {
						rows.Close()
						return diffs, fmt.Errorf("recon: R-03 scan: %w", err)
					}
					if havePrev && before != prevAfter {
						diffs = append(diffs, Diff{
							BizDate: bizDate, DiffType: DiffR03,
							Subject: strconv.FormatInt(prevID, 10), RequestID: requestID.String,
							LocalValue: prevAfter, UpstreamValue: before, DeltaMicro: before - prevAfter,
							MatchMode: MatchModeInternal, Level: LevelCritical,
						})
					}
					prevID, prevAfter, havePrev = id, after, true
					lastID = id
					n++
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					return diffs, fmt.Errorf("recon: R-03 rows: %w", err)
				}
				rows.Close()
				if n < DailyLedgerPage {
					break
				}
			}
		}
	}
	return diffs, nil
}

// R-04: batch remaining == face - consumed. No tolerance.
func checkR04(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	const q = `SELECT id, user_id, remaining_micro, face_value_micro, consumed_micro
	             FROM t_credit_batch
	            WHERE user_id >= ? AND user_id < ?
	              AND remaining_micro <> face_value_micro - consumed_micro`
	return d.scanDiffs(ctx, "t_credit_batch", q,
		func(rows *sql.Rows) (Diff, error) {
			var id, userID, remaining, face, consumed int64
			if err := rows.Scan(&id, &userID, &remaining, &face, &consumed); err != nil {
				return Diff{}, err
			}
			want := face - consumed
			return Diff{
				BizDate: bizDate, DiffType: DiffR04, Subject: strconv.FormatInt(id, 10),
				LocalValue: remaining, UpstreamValue: want, DeltaMicro: remaining - want,
				MatchMode: MatchModeInternal, Level: LevelHigh,
			}, nil
		})
}

// R-05: quota snapshot == DB usage for the CST day. delta = quota - usage.
// The two thresholds are the TDS values: >100 micro HIGH, >10,000 CRITICAL.
func checkR05(ctx context.Context, d *Daily, bizDate string, dayStart, dayEnd time.Time) ([]Diff, error) {
	var diffs []Diff
	lo, hi := d.shardBounds()
	maxUser, err := d.maxUser(ctx, "t_user_daily_quota", "stat_date = ?", bizDate)
	if err != nil {
		return nil, err
	}
	for shardLo := lo; shardLo <= maxUser; shardLo += d.shardSize() {
		shardHi := shardLo + d.shardSize()
		if shardHi > hi {
			shardHi = hi
		}
		rows, err := d.DB.QueryContext(ctx,
			`SELECT user_id, used_total_micro
			   FROM t_user_daily_quota
			  WHERE stat_date = ? AND user_id >= ? AND user_id < ?`, bizDate, shardLo, shardHi)
		if err != nil {
			return nil, fmt.Errorf("recon: R-05 quota page: %w", err)
		}
		type quotaRow struct{ uid, used int64 }
		var quota []quotaRow
		for rows.Next() {
			var uid, used int64
			if err := rows.Scan(&uid, &used); err != nil {
				rows.Close()
				return nil, fmt.Errorf("recon: R-05 scan: %w", err)
			}
			quota = append(quota, quotaRow{uid, used})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("recon: R-05 rows: %w", err)
		}
		rows.Close()

		for _, q := range quota {
			var usage int64
			err := d.DB.QueryRowContext(ctx,
				`SELECT COALESCE(SUM(consumed_micro), 0)
				   FROM t_usage_record
				  WHERE user_id = ? AND created_at >= ? AND created_at < ?`,
				q.uid, dayStart.UTC(), dayEnd.UTC(),
			).Scan(&usage)
			if err != nil {
				return nil, fmt.Errorf("recon: R-05 usage user=%d: %w", q.uid, err)
			}
			delta := q.used - usage
			level := ""
			switch {
			case abs64(delta) > R05CriticalMicro:
				level = LevelCritical
			case abs64(delta) > R05HighMicro:
				level = LevelHigh
			}
			if level == "" {
				continue
			}
			diffs = append(diffs, Diff{
				BizDate: bizDate, DiffType: DiffR05, Subject: strconv.FormatInt(q.uid, 10),
				LocalValue: q.used, UpstreamValue: usage, DeltaMicro: delta,
				MatchMode: MatchModeInternal, Level: level,
			})
		}
	}
	return diffs, nil
}

// R-06: expired HELD older than one hour. Any row is HIGH.
func checkR06(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	q := `SELECT COUNT(*) FROM t_prehold_hold
	       WHERE state = 'HELD'
	         AND expires_at < UTC_TIMESTAMP(3) - INTERVAL 1 HOUR`
	var args []any
	if d.UserLo != 0 || d.UserHi != 0 {
		lo, hi := d.shardBounds()
		q += ` AND user_id >= ? AND user_id < ?`
		args = append(args, lo, hi)
	}
	var n int64
	err := d.DB.QueryRowContext(ctx, q, args...).Scan(&n)
	if err != nil {
		return nil, fmt.Errorf("recon: R-06: %w", err)
	}
	if n == 0 {
		return nil, nil
	}
	return []Diff{{
		BizDate: bizDate, DiffType: DiffR06, Subject: "GLOBAL",
		LocalValue: n, UpstreamValue: 0, DeltaMicro: n,
		MatchMode: MatchModeInternal, Level: LevelHigh,
	}}, nil
}

// R-07: expired idempotency rows above the TDS threshold (100,000).
func checkR07(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	q := `SELECT COUNT(*) FROM t_idempotency_record WHERE expires_at < UTC_TIMESTAMP(3)`
	var args []any
	if d.UserLo != 0 || d.UserHi != 0 {
		lo, hi := d.shardBounds()
		q += ` AND user_id >= ? AND user_id < ?`
		args = append(args, lo, hi)
	}
	var n int64
	err := d.DB.QueryRowContext(ctx, q, args...).Scan(&n)
	if err != nil {
		return nil, fmt.Errorf("recon: R-07: %w", err)
	}
	threshold := d.IdemThreshold
	if threshold <= 0 {
		threshold = DefaultIdemExpiredThreshold
	}
	if n <= int64(threshold) {
		return nil, nil
	}
	return []Diff{{
		BizDate: bizDate, DiffType: DiffR07, Subject: "GLOBAL",
		LocalValue: n, UpstreamValue: int64(threshold), DeltaMicro: n - int64(threshold),
		MatchMode: MatchModeInternal, Level: LevelMedium,
	}}, nil
}

// R-08: gross profit == consumed - cost for rows with a configured cost.
func checkR08(ctx context.Context, d *Daily, bizDate string, _, _ time.Time) ([]Diff, error) {
	const q = `SELECT model_id, consumed_micro, cost_micro, gross_profit_micro
	             FROM t_metric_model_daily
	            WHERE stat_date = ? AND cost_configured = 1
	              AND gross_profit_micro <> consumed_micro - cost_micro`
	rows, err := d.DB.QueryContext(ctx, q, bizDate)
	if err != nil {
		return nil, fmt.Errorf("recon: R-08: %w", err)
	}
	defer rows.Close()
	var diffs []Diff
	for rows.Next() {
		var modelID, consumed, cost, gross int64
		if err := rows.Scan(&modelID, &consumed, &cost, &gross); err != nil {
			return nil, fmt.Errorf("recon: R-08 scan: %w", err)
		}
		want := consumed - cost
		diffs = append(diffs, Diff{
			BizDate: bizDate, DiffType: DiffR08, Subject: strconv.FormatInt(modelID, 10),
			LocalValue: gross, UpstreamValue: want, DeltaMicro: gross - want,
			MatchMode: MatchModeInternal, Level: LevelHigh,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recon: R-08 rows: %w", err)
	}
	return diffs, nil
}

// scanDiffs walks user shards and calls scan for every row.
func (d *Daily) scanDiffs(
	ctx context.Context,
	table, q string,
	scan func(*sql.Rows) (Diff, error),
) ([]Diff, error) {
	var diffs []Diff
	lo, hi := d.shardBounds()
	maxUser, err := d.maxUser(ctx, table, "")
	if err != nil {
		return nil, err
	}
	for shardLo := lo; shardLo <= maxUser; shardLo += d.shardSize() {
		shardHi := shardLo + d.shardSize()
		if shardHi > hi {
			shardHi = hi
		}
		rows, err := d.DB.QueryContext(ctx, q, shardLo, shardHi)
		if err != nil {
			return nil, fmt.Errorf("recon: shard %d: %w", shardLo, err)
		}
		for rows.Next() {
			diff, err := scan(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			diffs = append(diffs, diff)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return diffs, nil
}

// maxUser is the last shard start. where is a condition without the WHERE
// keyword; the caller's scope is always intersected with it.
func (d *Daily) maxUser(ctx context.Context, table, where string, args ...any) (int64, error) {
	lo, hi := d.shardBounds()
	q := "SELECT COALESCE(MAX(user_id), 0) FROM " + table +
		" WHERE user_id >= ? AND user_id < ?"
	if where != "" {
		q += " AND " + where
	}
	qargs := append([]any{lo, hi}, args...)
	var max int64
	if err := d.DB.QueryRowContext(ctx, q, qargs...).Scan(&max); err != nil {
		return 0, fmt.Errorf("recon: max user from %s: %w", table, err)
	}
	return max, nil
}

func (d *Daily) userIDs(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := d.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("recon: list users: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---- External bill comparison (TDS §5.4.1) ------------------------------

type localUsage struct {
	requestID string
	channelID int64
	modelName string
	tokensIn  int64
	tokensOut int64
	consumed  int64
	matched   bool
}

type upstreamBill struct {
	requestID string
	tradeNo   string
	billNo    string
	channelID int64
	modelName string
	tokensIn  int64
	tokensOut int64
	amount    int64
}

// runExternal compares t_upstream_bill with t_usage_record. It returns the
// diffs, whether the run is P0, and the reason.
func (d *Daily) runExternal(ctx context.Context, dayStart, dayEnd time.Time) ([]Diff, bool, string, error) {
	bizDate := dayStart.Format("2006-01-02")
	locals, err := d.loadLocalUsage(ctx, dayStart, dayEnd)
	if err != nil {
		return nil, false, "", err
	}
	bills, err := d.loadBills(ctx, bizDate)
	if err != nil {
		return nil, false, "", err
	}
	if len(bills) == 0 {
		// No bill ingested for this day. Emitting MISSING_UPSTREAM for every
		// local request would be noise, not a finding.
		return nil, false, "", nil
	}

	var diffs []Diff
	byReq := make(map[string]*localUsage, len(locals))
	for i := range locals {
		byReq[locals[i].requestID] = &locals[i]
	}
	approx := make(map[string][]*localUsage)
	for i := range locals {
		key := approxKey(locals[i].channelID, locals[i].modelName, locals[i].tokensIn, locals[i].tokensOut)
		approx[key] = append(approx[key], &locals[i])
	}
	billChannels := map[int64]bool{}
	for _, b := range bills {
		billChannels[b.channelID] = true
	}

	var missingLocal, missingUpstream int
	var sumAbsDelta int64
	for _, b := range bills {
		if b.requestID != "" {
			if l, ok := byReq[b.requestID]; ok {
				l.matched = true
				ds := compareBill(b, l, MatchModeDaily, bizDate)
				diffs = append(diffs, ds...)
				for _, diff := range ds {
					if isMoneyDiff(diff.DiffType) {
						sumAbsDelta += abs64(diff.DeltaMicro)
					}
				}
				continue
			}
		} else {
			key := approxKey(b.channelID, b.modelName, b.tokensIn, b.tokensOut)
			if cands := approx[key]; len(cands) > 0 {
				l := cands[0]
				approx[key] = cands[1:]
				l.matched = true
				ds := compareBill(b, l, MatchModeApprox, bizDate)
				diffs = append(diffs, ds...)
				for _, diff := range ds {
					if isMoneyDiff(diff.DiffType) {
						sumAbsDelta += abs64(diff.DeltaMicro)
					}
				}
				continue
			}
		}
		missingLocal++
		diff := Diff{
			BizDate: bizDate, DiffType: DiffMissingLocal, Subject: billSubject(b),
			RequestID: b.requestID, TradeNo: b.tradeNo,
			LocalValue: 0, UpstreamValue: b.amount, DeltaMicro: b.amount,
			MatchMode: matchModeFor(b.requestID), Level: LevelP1,
		}
		if abs64(diff.DeltaMicro) > billing.MicroPerYuan {
			diff.Level = LevelP0
		}
		diffs = append(diffs, diff)
		sumAbsDelta += abs64(diff.DeltaMicro)
	}

	for i := range locals {
		l := &locals[i]
		if l.matched || !billChannels[l.channelID] {
			continue
		}
		missingUpstream++
		diff := Diff{
			BizDate: bizDate, DiffType: DiffMissingUpstream, Subject: l.requestID,
			RequestID:  l.requestID,
			LocalValue: l.consumed, UpstreamValue: 0, DeltaMicro: -l.consumed,
			MatchMode: MatchModeDaily, Level: LevelP1,
		}
		if abs64(diff.DeltaMicro) > billing.MicroPerYuan {
			diff.Level = LevelP0
		}
		diffs = append(diffs, diff)
		sumAbsDelta += abs64(diff.DeltaMicro)
	}

	p0 := missingLocal > 0 || sumAbsDelta > 100*billing.MicroPerYuan
	reason := ""
	if p0 {
		reason = fmt.Sprintf("missing_local=%d missing_upstream=%d sum_abs_delta_micro=%d",
			missingLocal, missingUpstream, sumAbsDelta)
	}
	return diffs, p0, reason, nil
}

// compareBill emits token and/or amount differences for one matched pair.
func compareBill(b upstreamBill, l *localUsage, mode, bizDate string) []Diff {
	var diffs []Diff
	subject := billSubject(b)
	localTokens := l.tokensIn + l.tokensOut
	upTokens := b.tokensIn + b.tokensOut
	if localTokens != upTokens || l.tokensIn != b.tokensIn || l.tokensOut != b.tokensOut {
		diffs = append(diffs, Diff{
			BizDate: bizDate, DiffType: DiffTokenMismatch, Subject: subject,
			RequestID: b.requestID, TradeNo: b.tradeNo,
			LocalValue: localTokens, UpstreamValue: upTokens, DeltaMicro: upTokens - localTokens,
			MatchMode: mode, Level: LevelP1,
		})
	}
	if l.consumed != b.amount {
		level := LevelP1
		delta := b.amount - l.consumed
		if abs64(delta) > billing.MicroPerYuan {
			level = LevelP0
		}
		diffs = append(diffs, Diff{
			BizDate: bizDate, DiffType: DiffAmountMismatch, Subject: subject,
			RequestID: b.requestID, TradeNo: b.tradeNo,
			LocalValue: l.consumed, UpstreamValue: b.amount, DeltaMicro: delta,
			MatchMode: mode, Level: level,
		})
	}
	return diffs
}

func billSubject(b upstreamBill) string {
	if s := firstNonEmpty(b.requestID, b.tradeNo, b.billNo); s != "" {
		return s
	}
	return fmt.Sprintf("bill:%d:%s:%d:%d", b.channelID, b.modelName, b.tokensIn, b.tokensOut)
}

func (d *Daily) loadLocalUsage(ctx context.Context, dayStart, dayEnd time.Time) ([]localUsage, error) {
	rows, err := d.DB.QueryContext(ctx,
		`SELECT request_id, COALESCE(channel_id, 0), COALESCE(model_name_snap, ''),
		        COALESCE(tokens_in, 0), COALESCE(tokens_out, 0), consumed_micro
		   FROM t_usage_record
		  WHERE created_at >= ? AND created_at < ? AND result = 'SUCCESS'`,
		dayStart.UTC(), dayEnd.UTC())
	if err != nil {
		return nil, fmt.Errorf("recon: load local usage: %w", err)
	}
	defer rows.Close()
	var out []localUsage
	for rows.Next() {
		var u localUsage
		if err := rows.Scan(&u.requestID, &u.channelID, &u.modelName,
			&u.tokensIn, &u.tokensOut, &u.consumed); err != nil {
			return nil, fmt.Errorf("recon: scan local usage: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *Daily) loadBills(ctx context.Context, bizDate string) ([]upstreamBill, error) {
	compact := bizDate[:4] + bizDate[5:7] + bizDate[8:10]
	rows, err := d.DB.QueryContext(ctx,
		`SELECT COALESCE(request_id, ''), COALESCE(upstream_trade_no, ''),
		        COALESCE(bill_no, ''), channel_id, COALESCE(model_name, ''),
		        COALESCE(tokens_in, 0), COALESCE(tokens_out, 0), amount_micro
		   FROM t_upstream_bill
		  WHERE bill_period IN (?, ?)`, bizDate, compact)
	if err != nil {
		return nil, fmt.Errorf("recon: load bills: %w", err)
	}
	defer rows.Close()
	var out []upstreamBill
	for rows.Next() {
		var b upstreamBill
		if err := rows.Scan(&b.requestID, &b.tradeNo, &b.billNo, &b.channelID,
			&b.modelName, &b.tokensIn, &b.tokensOut, &b.amount); err != nil {
			return nil, fmt.Errorf("recon: scan bill: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func matchModeFor(requestID string) string {
	if requestID == "" {
		return MatchModeApprox
	}
	return MatchModeDaily
}

func isMoneyDiff(diffType string) bool {
	return diffType == DiffAmountMismatch || diffType == DiffMissingLocal || diffType == DiffMissingUpstream
}

func approxKey(channelID int64, model string, tokensIn, tokensOut int64) string {
	return fmt.Sprintf("%d|%s|%d|%d", channelID, model, tokensIn, tokensOut)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
