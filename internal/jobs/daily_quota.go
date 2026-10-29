package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// DefaultQuotaBackfillDays is how many completed CST days RunRecent inspects.
// Seven days means a jobsd outage of up to a week repairs itself on the next
// successful tick. Yesterday is always rebuilt; older days only when a user
// still has no row for them.
const DefaultQuotaBackfillDays = 7

// DailyQuota writes t_user_daily_quota: one row per (user, UTC+8 day) holding
// the day's starting quota snapshot and the day's consumption. R-05 reads this
// table and compares used_total_micro with SUM(t_usage_record.consumed_micro);
// before this job existed the table stayed empty and R-05 had no sample.
//
// Every write is an upsert on uk_udq_user_date, so re-running a day converges
// on the same values instead of duplicating rows.
type DailyQuota struct {
	DB       *sql.DB
	Logger   *obslog.Logger
	Clock    clock.Clock
	Location *time.Location
	// BackfillDays overrides DefaultQuotaBackfillDays when positive.
	BackfillDays int
}

// DailyQuotaResult is one Run or RunRecent.
type DailyQuotaResult struct {
	// StatDates are the days actually written, oldest first.
	StatDates []string
	// Rows is the number of users upserted across StatDates.
	Rows int64
	// SplitMissing counts rows where consumed_micro > 0 but both split columns
	// are zero. The split (T09) has not landed for those rows; used_total_micro
	// is still correct because R-05 compares only that column.
	SplitMissing int64
}

// quotaDay is one UTC+8 natural day.
type quotaDay struct {
	statDate string
	start    time.Time
	end      time.Time
}

// quotaUsage is one user's aggregate for a day.
type quotaUsage struct {
	consumed int64
	recharge int64
	bonus    int64
	requests int64
	blocked  int64
}

// NewDailyQuota builds the task.
func NewDailyQuota(db *sql.DB, logger *obslog.Logger, clk clock.Clock) *DailyQuota {
	if clk == nil {
		clk = clock.Real{}
	}
	return &DailyQuota{DB: db, Logger: logger, Clock: clk}
}

// Run writes one CST natural day. jobsd passes yesterday at 00:30; the day may
// be any past date so an operator can repair a specific date by calling Run.
func (q *DailyQuota) Run(ctx context.Context, day time.Time) (DailyQuotaResult, error) {
	var out DailyQuotaResult
	if q == nil || q.DB == nil {
		return out, fmt.Errorf("jobs: daily quota requires a database")
	}
	d := q.boundaries(day)
	out.StatDates = []string{d.statDate}

	usage, err := q.usageByUser(ctx, d)
	if err != nil {
		return out, err
	}
	changed, err := q.quotaChangeUsers(ctx, d)
	if err != nil {
		return out, err
	}

	users := make([]int64, 0, len(usage)+len(changed))
	for uid := range usage {
		users = append(users, uid)
	}
	for _, uid := range changed {
		if _, ok := usage[uid]; !ok {
			users = append(users, uid)
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })

	now := q.Clock.Now().UTC()
	tx, err := q.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("jobs: daily quota begin %s: %w", d.statDate, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, uid := range users {
		limit, err := q.resolveLimitMicro(ctx, tx, uid, d.start, d.statDate)
		if err != nil {
			return out, err
		}
		u := usage[uid]
		recharge, bonus, total, splitMissing := splitQuotaUsage(u.consumed, u.recharge, u.bonus)
		if splitMissing {
			out.SplitMissing++
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO t_user_daily_quota
			   (user_id, stat_date, limit_micro, used_recharge_micro, used_bonus_micro,
			    used_total_micro, request_count, blocked_count, reset_at, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?)
			 ON DUPLICATE KEY UPDATE
			   limit_micro = VALUES(limit_micro),
			   used_recharge_micro = VALUES(used_recharge_micro),
			   used_bonus_micro = VALUES(used_bonus_micro),
			   used_total_micro = VALUES(used_total_micro),
			   request_count = VALUES(request_count),
			   blocked_count = VALUES(blocked_count),
			   reset_at = VALUES(reset_at),
			   updated_at = VALUES(updated_at)`,
			uid, d.statDate, limit, recharge, bonus, total,
			u.requests, u.blocked, d.end.UTC(), now, now,
		); err != nil {
			return out, fmt.Errorf("jobs: daily quota upsert user=%d date=%s: %w", uid, d.statDate, err)
		}
		out.Rows++
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("jobs: daily quota commit %s: %w", d.statDate, err)
	}

	if q.Logger != nil {
		q.Logger.Info(ctx, fmt.Sprintf("daily_quota done date=%s users=%d split_missing=%d",
			d.statDate, out.Rows, out.SplitMissing), obslog.Fields{Stage: "daily_quota"})
		if out.SplitMissing > 0 {
			q.Logger.Warn(ctx, fmt.Sprintf(
				"daily_quota split_missing date=%s users=%d: consumed_recharge/bonus both zero while consumed_micro > 0; used_total_micro holds the total (T09 split not landed)",
				d.statDate, out.SplitMissing), obslog.Fields{Stage: "daily_quota"})
		}
	}
	return out, nil
}

// RunRecent is the scheduled entry point. It always rebuilds yesterday and
// recomputes any of the previous BackfillDays days that still have a user
// without a row, which is how a stopped jobsd heals itself after a restart.
func (q *DailyQuota) RunRecent(ctx context.Context) (DailyQuotaResult, error) {
	var out DailyQuotaResult
	if q == nil || q.DB == nil {
		return out, fmt.Errorf("jobs: daily quota requires a database")
	}
	loc := q.location()
	local := q.Clock.Now().UTC().In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	days := q.BackfillDays
	if days <= 0 {
		days = DefaultQuotaBackfillDays
	}
	for i := 1; i <= days; i++ {
		day := today.AddDate(0, 0, -i)
		if i > 1 {
			missing, err := q.dayHasGap(ctx, q.boundaries(day))
			if err != nil {
				return out, err
			}
			if !missing {
				continue
			}
		}
		res, err := q.Run(ctx, day)
		if err != nil {
			return out, err
		}
		out.StatDates = append(out.StatDates, res.StatDates...)
		out.Rows += res.Rows
		out.SplitMissing += res.SplitMissing
	}
	return out, nil
}

func (q *DailyQuota) location() *time.Location {
	if q.Location != nil {
		return q.Location
	}
	return time.FixedZone("CST", 8*3600)
}

// boundaries converts an instant to its UTC+8 natural day. reset_at is the
// next day's 00:00:00.000 UTC+8, the same rule the Redis counter TTL uses.
func (q *DailyQuota) boundaries(day time.Time) quotaDay {
	loc := q.location()
	local := day.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	return quotaDay{statDate: start.Format("2006-01-02"), start: start, end: end}
}

// usageByUser aggregates the request log for the day. created_at is datetime(3)
// holding UTC, so the day window is passed in UTC; the CST fold already
// happened in boundaries. request_count counts every row, including 429/402
// rejections, matching t_metric_minute.request_total's definition. blocked_count
// is the 429 subset.
func (q *DailyQuota) usageByUser(ctx context.Context, d quotaDay) (map[int64]quotaUsage, error) {
	rows, err := q.DB.QueryContext(ctx,
		`SELECT user_id,
		        COALESCE(SUM(consumed_micro), 0),
		        COALESCE(SUM(consumed_recharge_micro), 0),
		        COALESCE(SUM(consumed_bonus_micro), 0),
		        COUNT(*),
		        COALESCE(SUM(http_status = 429), 0)
		   FROM t_usage_record
		  WHERE created_at >= ? AND created_at < ?
		  GROUP BY user_id`,
		d.start.UTC(), d.end.UTC())
	if err != nil {
		return nil, fmt.Errorf("jobs: daily quota usage %s: %w", d.statDate, err)
	}
	defer rows.Close()

	out := map[int64]quotaUsage{}
	for rows.Next() {
		var uid int64
		var u quotaUsage
		if err := rows.Scan(&uid, &u.consumed, &u.recharge, &u.bonus, &u.requests, &u.blocked); err != nil {
			return nil, fmt.Errorf("jobs: daily quota usage scan %s: %w", d.statDate, err)
		}
		out[uid] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: daily quota usage rows %s: %w", d.statDate, err)
	}
	return out, nil
}

// quotaChangeUsers lists users whose USER-scope quota window opened or closed
// inside the day. Without them a user who was granted an upgrade but spent
// nothing would have no row, and the R-05 denominator would lose that day.
// scope_id = 0 is the tier-template sentinel seeded by 0018, not a user.
func (q *DailyQuota) quotaChangeUsers(ctx context.Context, d quotaDay) ([]int64, error) {
	rows, err := q.DB.QueryContext(ctx,
		`SELECT DISTINCT scope_id
		   FROM t_rate_limit_policy
		  WHERE scope_type = 'USER' AND scope_id > 0
		    AND ((effective_from >= ? AND effective_from < ?)
		      OR (effective_to >= ? AND effective_to < ?))`,
		d.start.UTC(), d.end.UTC(), d.start.UTC(), d.end.UTC())
	if err != nil {
		return nil, fmt.Errorf("jobs: daily quota changes %s: %w", d.statDate, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("jobs: daily quota changes scan %s: %w", d.statDate, err)
		}
		out = append(out, uid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: daily quota changes rows %s: %w", d.statDate, err)
	}
	return out, nil
}

// dayHasGap reports whether any user of that day has no snapshot row. It is
// the backfill trigger; a day whose rows all exist is left untouched so the
// nightly job does not rewrite a week of history every night.
func (q *DailyQuota) dayHasGap(ctx context.Context, d quotaDay) (bool, error) {
	var missing int
	err := q.DB.QueryRowContext(ctx,
		`SELECT EXISTS(
		   SELECT 1
		     FROM (
		       SELECT user_id
		         FROM t_usage_record
		        WHERE created_at >= ? AND created_at < ?
		        GROUP BY user_id
		       UNION
		       SELECT scope_id AS user_id
		         FROM t_rate_limit_policy
		        WHERE scope_type = 'USER' AND scope_id > 0
		          AND ((effective_from >= ? AND effective_from < ?)
		            OR (effective_to >= ? AND effective_to < ?))
		     ) s
		     LEFT JOIN t_user_daily_quota q
		       ON q.user_id = s.user_id AND q.stat_date = ?
		    WHERE q.user_id IS NULL)`,
		d.start.UTC(), d.end.UTC(),
		d.start.UTC(), d.end.UTC(), d.start.UTC(), d.end.UTC(),
		d.statDate,
	).Scan(&missing)
	if err != nil {
		return false, fmt.Errorf("jobs: daily quota gap %s: %w", d.statDate, err)
	}
	return missing != 0, nil
}

// rowQueryer is the subset of *sql.DB / *sql.Tx the limit lookup needs.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// resolveLimitMicro returns the USER daily quota in force at the day's start,
// read from t_rate_limit_policy.daily_quota_micro with the same resolution
// order as control/fund.currentDailyQuota: newest active USER policy row,
// else the plan row's daily_quota_micro through t_user.plan_id.
//
// Both sources can hold NULL, which means "no ceiling" (FREE_DEFAULT).
// t_user_daily_quota.limit_micro is NOT NULL, so a NULL limit is stored as 0 —
// the value the readers already treat as "no limit" (a zero denominator).
// When no policy changed, resolving at day start yields the previous day's
// value; that is why the job never copies yesterday's row.
func (q *DailyQuota) resolveLimitMicro(ctx context.Context, db rowQueryer, uid int64, at time.Time, statDate string) (int64, error) {
	at = at.UTC()
	var policyQuota sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT daily_quota_micro
		   FROM t_rate_limit_policy
		  WHERE scope_type = 'USER' AND scope_id = ?
		    AND effective_from <= ?
		    AND (effective_to IS NULL OR effective_to > ?)
		  ORDER BY effective_from DESC, id DESC
		  LIMIT 1`,
		uid, at, at,
	).Scan(&policyQuota)
	switch {
	case err == nil:
		if !policyQuota.Valid {
			return 0, nil
		}
		return policyQuota.Int64, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("jobs: daily quota policy user=%d date=%s: %w", uid, statDate, err)
	}

	var planQuota sql.NullInt64
	err = db.QueryRowContext(ctx,
		`SELECT p.daily_quota_micro
		   FROM t_user u
		   LEFT JOIN t_plan p ON p.id = u.plan_id
		  WHERE u.id = ? AND u.deleted_at IS NULL`,
		uid,
	).Scan(&planQuota)
	if errors.Is(err, sql.ErrNoRows) {
		// Usage without a user row happens in fixtures and after a hard delete;
		// there is no ceiling to snapshot.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("jobs: daily quota plan user=%d date=%s: %w", uid, statDate, err)
	}
	// NULL plan quota (FREE_DEFAULT) scans as an invalid NullInt64 and maps to 0.
	return planQuota.Int64, nil
}

// splitQuotaUsage applies the T09 data-gap rule: when both split columns are
// zero but the total is positive, the split has not landed. Do not apportion
// it — write the total and leave the split at zero, and let the caller report
// the gap. R-05 compares used_total_micro only.
func splitQuotaUsage(total, recharge, bonus int64) (r, b, t int64, splitMissing bool) {
	if total > 0 && recharge == 0 && bonus == 0 {
		return 0, 0, total, true
	}
	return recharge, bonus, total, false
}
