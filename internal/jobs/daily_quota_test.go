package jobs

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

// quotaTestClock is mutable so a test can move updated_at between two runs of
// the same day without sleeping.
type quotaTestClock struct{ at time.Time }

func (c *quotaTestClock) Now() time.Time { return c.at }

func cleanupDailyQuota(t *testing.T, users ...int64) {
	t.Helper()
	for _, uid := range users {
		for _, stmt := range []string{
			`DELETE FROM t_user_daily_quota WHERE user_id = ?`,
			`DELETE FROM t_usage_record WHERE user_id = ?`,
			`DELETE FROM t_rate_limit_policy WHERE scope_type = 'USER' AND scope_id = ?`,
		} {
			if _, err := testDB.Exec(stmt, uid); err != nil {
				t.Fatalf("daily quota cleanup user=%d: %v\nquery: %s", uid, err, stmt)
			}
		}
	}
}

func insertQuotaUsage(t *testing.T, rid string, user int64, at time.Time,
	consumed, recharge, bonus int64, status int) {
	t.Helper()
	if _, err := testDB.Exec(
		`INSERT INTO t_usage_record
		   (request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
		    request_model_req, inbound_protocol, http_status, result,
		    consumed_micro, consumed_recharge_micro, consumed_bonus_micro,
		    total_ms, created_at)
		 VALUES (?,?,1,'quota-fixture','quota-fixture','quota-fixture','OPENAI_COMPAT',
		         ?,'SUCCESS',?,?,?,10,?)`,
		rid, user, status, consumed, recharge, bonus, at.UTC()); err != nil {
		t.Fatalf("insert quota usage %s: %v", rid, err)
	}
}

// insertQuotaPolicy writes one USER-scope policy. A nil daily value stores SQL
// NULL, the FREE_DEFAULT "no ceiling" marker.
func insertQuotaPolicy(t *testing.T, user int64, daily sql.NullInt64, from time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := testDB.Exec(
		`INSERT INTO t_rate_limit_policy
		   (scope_type, scope_id, source, rpm, tpm, concurrency_quota,
		    daily_quota_micro, effective_from, effective_to, fallback_policy_id,
		    created_at, updated_at)
		 VALUES ('USER', ?, 'PLAN', 60, 60000, 5, ?, ?, NULL, NULL, ?, ?)`,
		user, daily, from.UTC(), now, now); err != nil {
		t.Fatalf("insert quota policy user=%d: %v", user, err)
	}
}

func quotaRow(t *testing.T, user int64, statDate string) (limit, recharge, bonus, total, requests, blocked int64, resetAt time.Time) {
	t.Helper()
	err := testDB.QueryRow(
		`SELECT limit_micro, used_recharge_micro, used_bonus_micro, used_total_micro,
		        request_count, blocked_count, reset_at
		   FROM t_user_daily_quota
		  WHERE user_id = ? AND stat_date = ?`, user, statDate,
	).Scan(&limit, &recharge, &bonus, &total, &requests, &blocked, &resetAt)
	if err != nil {
		t.Fatalf("read quota row user=%d date=%s: %v", user, statDate, err)
	}
	return
}

// ① One user's usage is persisted with the day's starting limit, the split as
// recorded, and request/blocked counts. ④'s split-gap rule is covered by the
// second user: consumed > 0 with both split columns zero must not be apportioned.
func TestDailyQuotaWritesDayUsage(t *testing.T) {
	ctx := context.Background()
	cst := time.FixedZone("CST", 8*3600)
	day := time.Date(2026, 6, 10, 0, 0, 0, 0, cst)
	const (
		user      = int64(970001)
		gapUser   = int64(970005)
		statDate  = "2026-06-10"
		dayMicros = int64(1_000_000)
	)
	cleanupDailyQuota(t, user, gapUser)
	t.Cleanup(func() { cleanupDailyQuota(t, user, gapUser) })

	// Active since 30 days before the day: a policy, but not a change that day,
	// so this user enters the run through usage alone.
	insertQuotaPolicy(t, user, sql.NullInt64{Int64: dayMicros, Valid: true}, day.AddDate(0, 0, -30))

	insertQuotaUsage(t, jobRID("qd01", 1), user, day.Add(9*time.Hour), 700, 500, 200, 200)
	insertQuotaUsage(t, jobRID("qd01", 2), user, day.Add(10*time.Hour), 300, 100, 200, 200)
	// Split not landed (T09): both split columns zero, total positive.
	insertQuotaUsage(t, jobRID("qd01", 3), gapUser, day.Add(11*time.Hour), 900, 0, 0, 200)

	task := NewDailyQuota(testDB, nil, clock.Real{})
	task.Location = cst
	res, err := task.Run(ctx, day)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Rows != 2 || res.SplitMissing != 1 {
		t.Fatalf("result = %+v; want rows=2 split_missing=1", res)
	}

	limit, recharge, bonus, total, requests, blocked, resetAt := quotaRow(t, user, statDate)
	if limit != dayMicros || recharge != 600 || bonus != 400 || total != 1000 ||
		requests != 2 || blocked != 0 {
		t.Fatalf("row = limit=%d split=%d/%d total=%d requests=%d blocked=%d; want %d/600/400/1000/2/0",
			limit, recharge, bonus, total, requests, blocked, dayMicros)
	}
	wantReset := day.AddDate(0, 0, 1).UTC()
	if !resetAt.UTC().Equal(wantReset) {
		t.Fatalf("reset_at = %s; want %s (= next 00:00 CST)", resetAt.UTC(), wantReset)
	}

	// No policy and no t_user row here: the limit snapshot falls back to 0,
	// while the total is still written from consumed_micro.
	gapLimit, gapRecharge, gapBonus, gapTotal, _, _, _ := quotaRow(t, gapUser, statDate)
	if gapLimit != 0 || gapRecharge != 0 || gapBonus != 0 || gapTotal != 900 {
		t.Fatalf("split-missing row = limit=%d split=%d/%d total=%d; want 0/0/0/900",
			gapLimit, gapRecharge, gapBonus, gapTotal)
	}
}

// ② Re-running the same day converges: one row, identical values, created_at
// preserved, updated_at advanced.
func TestDailyQuotaRerunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	cst := time.FixedZone("CST", 8*3600)
	day := time.Date(2026, 6, 12, 0, 0, 0, 0, cst)
	const (
		user     = int64(970002)
		statDate = "2026-06-12"
	)
	cleanupDailyQuota(t, user)
	t.Cleanup(func() { cleanupDailyQuota(t, user) })

	insertQuotaPolicy(t, user, sql.NullInt64{Int64: 2_000_000, Valid: true}, day.AddDate(0, 0, -1))
	insertQuotaUsage(t, jobRID("qd02", 1), user, day.Add(8*time.Hour), 500, 500, 0, 200)

	clk := &quotaTestClock{at: time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)}
	task := NewDailyQuota(testDB, nil, clk)
	task.Location = cst
	if _, err := task.Run(ctx, day); err != nil {
		t.Fatalf("first run: %v", err)
	}

	var (
		firstCount   int
		firstTotal   int64
		firstCreated time.Time
		firstUpdated time.Time
	)
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM t_user_daily_quota WHERE user_id = ? AND stat_date = ?`,
		user, statDate).Scan(&firstCount); err != nil {
		t.Fatalf("count after first run: %v", err)
	}
	if err := testDB.QueryRow(
		`SELECT used_total_micro, created_at, updated_at FROM t_user_daily_quota
		  WHERE user_id = ? AND stat_date = ?`, user, statDate,
	).Scan(&firstTotal, &firstCreated, &firstUpdated); err != nil {
		t.Fatalf("read after first run: %v", err)
	}

	clk.at = clk.at.Add(time.Second)
	res, err := task.Run(ctx, day)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("second run rows = %d; want 1", res.Rows)
	}

	var secondCount int
	var secondTotal int64
	var secondCreated, secondUpdated time.Time
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM t_user_daily_quota WHERE user_id = ? AND stat_date = ?`,
		user, statDate).Scan(&secondCount); err != nil {
		t.Fatalf("count after second run: %v", err)
	}
	if err := testDB.QueryRow(
		`SELECT used_total_micro, created_at, updated_at FROM t_user_daily_quota
		  WHERE user_id = ? AND stat_date = ?`, user, statDate,
	).Scan(&secondTotal, &secondCreated, &secondUpdated); err != nil {
		t.Fatalf("read after second run: %v", err)
	}

	if firstCount != 1 || secondCount != 1 {
		t.Fatalf("row count %d -> %d; want 1 -> 1", firstCount, secondCount)
	}
	if firstTotal != 500 || secondTotal != 500 {
		t.Fatalf("used_total %d -> %d; want 500 both runs", firstTotal, secondTotal)
	}
	if !secondCreated.UTC().Equal(firstCreated.UTC()) {
		t.Fatalf("created_at changed %s -> %s; upsert must not reset it", firstCreated.UTC(), secondCreated.UTC())
	}
	if !secondUpdated.UTC().After(firstUpdated.UTC()) {
		t.Fatalf("updated_at did not advance: %s -> %s", firstUpdated.UTC(), secondUpdated.UTC())
	}
}

// ③ The CST day boundary, not UTC: 2026-10-28T16:30Z is 10-29 00:30 CST and
// belongs to stat_date 10-29, while 15:30Z the same UTC day belongs to 10-28.
func TestDailyQuotaUsesCSTDayBoundary(t *testing.T) {
	ctx := context.Background()
	cst := time.FixedZone("CST", 8*3600)
	day28 := time.Date(2026, 10, 28, 0, 0, 0, 0, cst)
	day29 := time.Date(2026, 10, 29, 0, 0, 0, 0, cst)
	const user = int64(970003)
	cleanupDailyQuota(t, user)
	t.Cleanup(func() { cleanupDailyQuota(t, user) })

	insertQuotaPolicy(t, user, sql.NullInt64{Int64: 1_000_000, Valid: true}, day28.AddDate(0, 0, -30))
	// 23:30 CST on 10-28 and 00:30 CST on 10-29, both inside UTC 10-28.
	insertQuotaUsage(t, jobRID("qd03", 1), user, time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC), 400, 400, 0, 200)
	insertQuotaUsage(t, jobRID("qd03", 2), user, time.Date(2026, 10, 28, 16, 30, 0, 0, time.UTC), 600, 600, 0, 200)

	task := NewDailyQuota(testDB, nil, clock.Real{})
	task.Location = cst
	if _, err := task.Run(ctx, day28); err != nil {
		t.Fatalf("run 10-28: %v", err)
	}

	_, _, _, total28, requests28, _, reset28 := quotaRow(t, user, "2026-10-28")
	if total28 != 400 || requests28 != 1 {
		t.Fatalf("10-28 row = total=%d requests=%d; want 400/1 (usage at 00:30 CST must not land here)",
			total28, requests28)
	}
	if want := day29.UTC(); !reset28.UTC().Equal(want) {
		t.Fatalf("10-28 reset_at = %s; want %s", reset28.UTC(), want)
	}
	var rows29 int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM t_user_daily_quota WHERE user_id = ? AND stat_date = ?`,
		user, "2026-10-29").Scan(&rows29); err != nil {
		t.Fatalf("count 10-29: %v", err)
	}
	if rows29 != 0 {
		t.Fatalf("10-29 rows = %d after running 10-28; want 0", rows29)
	}

	if _, err := task.Run(ctx, day29); err != nil {
		t.Fatalf("run 10-29: %v", err)
	}
	_, _, _, total29, requests29, _, _ := quotaRow(t, user, "2026-10-29")
	if total29 != 600 || requests29 != 1 {
		t.Fatalf("10-29 row = total=%d requests=%d; want 600/1", total29, requests29)
	}
	// Re-running 10-28 must not pick up the later request.
	if _, _, _, again28, _, _, _ := quotaRow(t, user, "2026-10-28"); again28 != 400 {
		t.Fatalf("10-28 total after re-run = %d; want 400", again28)
	}
}

// ④ A user who was granted a quota starting exactly at the day boundary and
// spent nothing still gets a row, with that ceiling as the starting snapshot.
func TestDailyQuotaWritesRowForZeroUsageGrant(t *testing.T) {
	ctx := context.Background()
	cst := time.FixedZone("CST", 8*3600)
	day := time.Date(2026, 6, 10, 0, 0, 0, 0, cst)
	const (
		user     = int64(970004)
		statDate = "2026-06-10"
		granted  = int64(2_000_000)
	)
	cleanupDailyQuota(t, user)
	t.Cleanup(func() { cleanupDailyQuota(t, user) })

	// nextDayUTC8-style grant: effective at 00:00 CST on the day itself.
	insertQuotaPolicy(t, user, sql.NullInt64{Int64: granted, Valid: true}, day)

	task := NewDailyQuota(testDB, nil, clock.Real{})
	task.Location = cst
	res, err := task.Run(ctx, day)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("rows = %d; want 1 (grant-only user must get a row)", res.Rows)
	}

	limit, recharge, bonus, total, requests, blocked, _ := quotaRow(t, user, statDate)
	if limit != granted || total != 0 || recharge != 0 || bonus != 0 || requests != 0 || blocked != 0 {
		t.Fatalf("row = limit=%d total=%d split=%d/%d requests=%d blocked=%d; want %d/0/0/0/0/0",
			limit, total, recharge, bonus, requests, blocked, granted)
	}
}
