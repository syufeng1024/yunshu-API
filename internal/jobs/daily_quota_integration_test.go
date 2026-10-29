package jobs

// This integration test needs a MySQL instance. It runs under the package
// TestMain, which owns schema yunshu_jobs_test and applies every migration; no
// extra setup is required beyond TEST_DB_DSN pointing at a MySQL host.
//
// T40-b 段 2：造一天真实用量，跑真实的 DailyQuota.Run 落 t_user_daily_quota，
// 再跑真实的 recon R-05 检查（recon.Daily.Checks() 中 R05_DAILY_QUOTA 这一项，
// 即 internal/recon/daily.go 的 checkR05），断言该日 diffs == 0 且逐用户
// used_total_micro == SUM(t_usage_record.consumed_micro)。
//
// 固定 user_id 作用域 [982000, 982100)，结束前清理本次造的数据。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/recon"
)

const (
	quotaIntegrationUserLo      = int64(982000)
	quotaIntegrationUserHi      = int64(982100)
	quotaIntegrationSplitUser   = int64(982001) // 正常 split 用量
	quotaIntegrationBlockedUser = int64(982002) // 含 429 被拦与 402 拒绝
	quotaIntegrationGapUser     = int64(982003) // T09 split 未回填：consumed>0，split 全 0
)

func quotaIntegrationDay() time.Time {
	cst := time.FixedZone("CST", 8*3600)
	return time.Date(2026, 9, 17, 0, 0, 0, 0, cst)
}

// seedQuotaIntegrationDay writes one CST day of usage for three users, plus a
// prior-day row per counted user so the counter-proof (usage window widened by
// one day) has a delta to expose.
func seedQuotaIntegrationDay(t *testing.T) time.Time {
	t.Helper()
	day := quotaIntegrationDay()
	users := []int64{
		quotaIntegrationSplitUser,
		quotaIntegrationBlockedUser,
		quotaIntegrationGapUser,
	}
	cleanupDailyQuota(t, users...)
	t.Cleanup(func() { cleanupDailyQuota(t, users...) })

	insertQuotaPolicy(t, quotaIntegrationSplitUser,
		sql.NullInt64{Int64: 5_000_000, Valid: true}, day.AddDate(0, 0, -30))
	insertQuotaPolicy(t, quotaIntegrationBlockedUser,
		sql.NullInt64{Int64: 2_000_000, Valid: true}, day.AddDate(0, 0, -30))

	// The day itself: split usage, a 429, a 402, and one T09 gap sample.
	insertQuotaUsage(t, jobRID("qdi1", 1), quotaIntegrationSplitUser,
		day.Add(9*time.Hour), 1_200_000, 700_000, 500_000, 200)
	insertQuotaUsage(t, jobRID("qdi1", 2), quotaIntegrationSplitUser,
		day.Add(10*time.Hour), 300_000, 300_000, 0, 200)
	insertQuotaUsage(t, jobRID("qdi1", 3), quotaIntegrationBlockedUser,
		day.Add(11*time.Hour), 400_000, 400_000, 0, 200)
	insertQuotaUsage(t, jobRID("qdi1", 4), quotaIntegrationBlockedUser,
		day.Add(11*time.Hour).Add(5*time.Minute), 0, 0, 0, 429)
	insertQuotaUsage(t, jobRID("qdi1", 5), quotaIntegrationBlockedUser,
		day.Add(12*time.Hour), 0, 0, 0, 402)
	insertQuotaUsage(t, jobRID("qdi1", 6), quotaIntegrationGapUser,
		day.Add(13*time.Hour), 900_000, 0, 0, 200)

	// Previous CST day: must stay out of the day-D snapshot.
	insertQuotaUsage(t, jobRID("qdi1", 7), quotaIntegrationSplitUser,
		day.AddDate(0, 0, -1).Add(20*time.Hour), 5_000_000, 5_000_000, 0, 200)
	insertQuotaUsage(t, jobRID("qdi1", 8), quotaIntegrationBlockedUser,
		day.AddDate(0, 0, -1).Add(20*time.Hour), 250_000, 250_000, 0, 200)

	return day
}

type quotaComparison struct {
	userID                            int64
	limit, recharge, bonus, usedTotal int64
	requests, blocked, usageSum       int64
}

// readQuotaComparison reads the day's snapshot for the test user band and the
// matching SUM(t_usage_record.consumed_micro) in the same CST window.
func readQuotaComparison(t *testing.T, day time.Time) []quotaComparison {
	t.Helper()
	rows, err := testDB.Query(
		`SELECT q.user_id, q.limit_micro, q.used_recharge_micro, q.used_bonus_micro,
		        q.used_total_micro, q.request_count, q.blocked_count,
		        COALESCE((SELECT SUM(u.consumed_micro)
		                    FROM t_usage_record u
		                   WHERE u.user_id = q.user_id
		                     AND u.created_at >= ? AND u.created_at < ?), 0)
		   FROM t_user_daily_quota q
		  WHERE q.stat_date = ? AND q.user_id >= ? AND q.user_id < ?
		  ORDER BY q.user_id`,
		day.UTC(), day.AddDate(0, 0, 1).UTC(),
		day.Format("2006-01-02"), quotaIntegrationUserLo, quotaIntegrationUserHi)
	if err != nil {
		t.Fatalf("read quota comparison: %v", err)
	}
	defer rows.Close()

	var out []quotaComparison
	for rows.Next() {
		var c quotaComparison
		if err := rows.Scan(&c.userID, &c.limit, &c.recharge, &c.bonus,
			&c.usedTotal, &c.requests, &c.blocked, &c.usageSum); err != nil {
			t.Fatalf("scan quota comparison: %v", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("quota comparison rows: %v", err)
	}
	return out
}

// runReconR05 invokes the real checkR05 through the exported Checks() table.
func runReconR05(t *testing.T, day time.Time) []recon.Diff {
	t.Helper()
	d := recon.NewDaily(testDB, nil, nil, clock.Real{}, "t40b")
	d.UserLo, d.UserHi = quotaIntegrationUserLo, quotaIntegrationUserHi

	var check recon.Check
	for _, c := range d.Checks() {
		if c.Code == recon.DiffR05 {
			check = c
			break
		}
	}
	if check.Run == nil {
		t.Fatal("recon: R-05 check not registered in Checks()")
	}
	diffs, err := check.Run(context.Background(), d, day.Format("2006-01-02"),
		day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("recon R-05: %v", err)
	}
	return diffs
}

func TestDailyQuotaR05Integration(t *testing.T) {
	ctx := context.Background()
	day := seedQuotaIntegrationDay(t)
	statDate := day.Format("2006-01-02")

	task := NewDailyQuota(testDB, nil, clock.Real{})
	task.Location = time.FixedZone("CST", 8*3600)
	res, err := task.Run(ctx, day)
	if err != nil {
		t.Fatalf("daily quota run: %v", err)
	}
	t.Logf("T40-b day=%s rows=%d split_missing=%d", statDate, res.Rows, res.SplitMissing)
	if res.Rows != 3 {
		t.Fatalf("rows = %d; want 3 (one row per seeded user)", res.Rows)
	}
	if res.SplitMissing != 1 {
		t.Fatalf("split_missing = %d; want 1 (T09 gap user)", res.SplitMissing)
	}

	comparisons := readQuotaComparison(t, day)
	t.Logf("user_id | used_total_micro | SUM(consumed_micro) | equal | limit_micro | request_count | blocked_count")
	for _, c := range comparisons {
		t.Logf("%d | %d | %d | %t | %d | %d | %d",
			c.userID, c.usedTotal, c.usageSum, c.usedTotal == c.usageSum,
			c.limit, c.requests, c.blocked)
	}

	want := map[int64]struct {
		total, requests, blocked int64
	}{
		quotaIntegrationSplitUser:   {1_500_000, 2, 0},
		quotaIntegrationBlockedUser: {400_000, 3, 1},
		quotaIntegrationGapUser:     {900_000, 1, 0},
	}
	if len(comparisons) != len(want) {
		t.Fatalf("snapshot rows = %d; want %d", len(comparisons), len(want))
	}
	for _, c := range comparisons {
		w, ok := want[c.userID]
		if !ok {
			t.Fatalf("unexpected user in snapshot: %d", c.userID)
		}
		if c.usedTotal != c.usageSum {
			t.Errorf("user=%d used_total_micro=%d != SUM(consumed_micro)=%d",
				c.userID, c.usedTotal, c.usageSum)
		}
		if c.usedTotal != w.total || c.requests != w.requests || c.blocked != w.blocked {
			t.Errorf("user=%d total/requests/blocked = %d/%d/%d; want %d/%d/%d",
				c.userID, c.usedTotal, c.requests, c.blocked, w.total, w.requests, w.blocked)
		}
	}

	diffs := runReconR05(t, day)
	t.Logf("R05_DAILY_QUOTA day=%s diffs=%d", statDate, len(diffs))
	for _, df := range diffs {
		t.Logf("R05 diff subject=%s local=%d upstream=%d delta=%d level=%s",
			df.Subject, df.LocalValue, df.UpstreamValue, df.DeltaMicro, df.Level)
	}
	if len(diffs) != 0 {
		t.Fatalf("R-05 returned %d diff(s); want 0", len(diffs))
	}
}
