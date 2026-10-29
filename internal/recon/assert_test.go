package recon

// T21 deliverable 8: one test per reconciliation assertion R-01..R-08, plus
// T38's settlement §2 additions (orphan ledger, batch sum, day-window
// conservation, unsettled success, config traceability). Each test plants
// exactly one violation and proves the check reports it. The existing
// TestDailyChecksR01toR08 is table-driven; these tests exist so a failing
// assertion can be selected by name (`go test -run R03`), which is how the
// nightly job attributes a red run.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func assertCheck(t *testing.T, code string, lo, hi int64, day time.Time, setup func(t *testing.T) wantDiff) {
	t.Helper()
	ctx := context.Background()
	bizDate := day.Format("2006-01-02")
	dayStart, dayEnd := day, day.AddDate(0, 0, 1)

	d := NewDaily(testDB, nil, nil, nil, "t21-assert")
	if d.Clock == nil {
		d.Clock = fixedClock{day: dayStart}
	}
	d.IdemThreshold = 1
	d.UserLo, d.UserHi = lo, hi

	cleanReconScope(t, lo, hi, bizDate)
	t.Cleanup(func() { cleanReconScope(t, lo, hi, bizDate) })

	want := setup(t)
	check, ok := checkByCode(d, code)
	if !ok {
		t.Fatalf("%s: check not registered", code)
	}
	diffs, err := check.Run(ctx, d, bizDate, dayStart, dayEnd)
	if err != nil {
		t.Fatalf("%s: run: %v", code, err)
	}
	if len(diffs) == 0 {
		t.Fatalf("%s: planted violation was not detected", code)
	}
	found := false
	for _, got := range diffs {
		if got.DiffType == want.DiffType && got.Subject == want.Subject &&
			got.LocalValue == want.Local && got.UpstreamValue == want.Upstream &&
			got.DeltaMicro == want.Delta {
			found = true
			if got.Level != want.Level || got.MatchMode != want.MatchMode {
				t.Fatalf("%s: level/match_mode=%s/%s, want %s/%s",
					code, got.Level, got.MatchMode, want.Level, want.MatchMode)
			}
		}
	}
	if !found {
		t.Fatalf("%s: expected diff %+v not in %+v", code, want, diffs)
	}
}

// checkByCode selects one registered check by its acceptance label.
func checkByCode(d *Daily, code string) (Check, bool) {
	for _, c := range d.Checks() {
		if c.Code == code {
			return c, true
		}
	}
	return Check{}, false
}

type fixedClock struct{ day time.Time }

func (c fixedClock) Now() time.Time { return c.day.Add(12 * time.Hour) }

func TestR01BalanceMismatchDetected(t *testing.T) {
	assertCheck(t, DiffR01, 903001, 903100, time.Date(2026, 10, 21, 0, 0, 0, 0, CST),
		func(t *testing.T) wantDiff {
			insertAccount(t, 903001, 1000, 0)
			return wantDiff{DiffR01, "903001", MatchModeInternal, LevelCritical, 1000, 0, 1000}
		})
}

func TestR02RechargeBonusMismatchDetected(t *testing.T) {
	assertCheck(t, DiffR02, 903101, 903200, time.Date(2026, 10, 21, 0, 0, 0, 0, CST),
		func(t *testing.T) wantDiff {
			insertAccountValues(t, 903101, 600, 300, 1000)
			insertBatch(t, 903101, 900, 900, 0)
			return wantDiff{DiffR02, "903101", MatchModeInternal, LevelCritical, 1000, 900, 100}
		})
}

func TestR03LedgerContinuityBreakDetected(t *testing.T) {
	assertCheck(t, DiffR03, 903201, 903300, time.Date(2026, 10, 21, 0, 0, 0, 0, CST),
		func(t *testing.T) wantDiff {
			req := rid("R03X", 1)
			insertLedgerEntry(t, 903201, entryNo("R03X", 1), "RECHARGE",
				"CREDIT", 0, 5000, 5000, 0, 5000, "t21r03:1", &req)
			insertLedgerEntry(t, 903201, entryNo("R03X", 2), "CONSUME_SETTLE",
				"DEBIT", 0, 0, 0, 4000, 4000, "t21r03:2", &req)
			var prevID int64
			if err := testDB.QueryRow(`SELECT id FROM t_ledger_entry WHERE entry_no = ?`,
				entryNo("R03X", 1)).Scan(&prevID); err != nil {
				t.Fatalf("read T21R03 entry: %v", err)
			}
			return wantDiff{DiffR03, fmt.Sprintf("%d", prevID), MatchModeInternal, LevelCritical, 5000, 4000, -1000}
		})
}

func TestR04BatchRemainingMismatchDetected(t *testing.T) {
	assertCheck(t, DiffR04, 903301, 903400, time.Date(2026, 10, 21, 0, 0, 0, 0, CST),
		func(t *testing.T) wantDiff {
			insertAccount(t, 903301, 5000, 0)
			insertLedgerEntry(t, 903301, entryNo("R04X", 1), "RECHARGE",
				"CREDIT", 0, 5000, 5000, 0, 5000, "t21r04:1", nil)
			execFixture(t,
				`INSERT INTO t_credit_batch
				   (user_id, batch_no, batch_type, face_value_micro, remaining_micro,
				    consumed_micro, refundable, effective_at, created_at, updated_at)
				 VALUES (?, '20261021000000009991', 'BONUS', 10000, 5000, 3000, 0, ?, ?, ?)`,
				903301, time.Now().UTC(), time.Now().UTC(), time.Now().UTC())
			var id int64
			if err := testDB.QueryRow(`SELECT id FROM t_credit_batch WHERE user_id = ?`, 903301).Scan(&id); err != nil {
				t.Fatalf("read T21R04 batch: %v", err)
			}
			return wantDiff{DiffR04, fmt.Sprintf("%d", id), MatchModeInternal, LevelHigh, 5000, 7000, -2000}
		})
}

func TestR05DailyQuotaMismatchDetected(t *testing.T) {
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, CST)
	assertCheck(t, DiffR05, 903401, 903500, day,
		func(t *testing.T) wantDiff {
			now := day.Add(10 * time.Hour)
			execFixture(t,
				`INSERT INTO t_user_daily_quota
				   (user_id, stat_date, limit_micro, used_total_micro, reset_at, created_at, updated_at)
				 VALUES (?,?,1000000,20000,?,?,?)`,
				903401, day.Format("2006-01-02"), day.AddDate(0, 0, 1).UTC(), now.UTC(), now.UTC())
			return wantDiff{DiffR05, "903401", MatchModeInternal, LevelCritical, 20000, 0, 20000}
		})
}

func TestR06HangingPreholdDetected(t *testing.T) {
	assertCheck(t, DiffR06, 903501, 903600, time.Date(2026, 10, 21, 0, 0, 0, 0, CST),
		func(t *testing.T) wantDiff {
			execFixture(t,
				`INSERT INTO t_prehold_hold
				   (request_id, user_id, api_key_id, hold_micro, estimated_micro, model_id,
				    state, expires_at, node_id, created_at)
				 VALUES (?, 903501, 1, 1000, 1000, 1, 'HELD', ?, 't21', ?)`,
				rid("R06X", 1), time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(-2*time.Hour))
			return wantDiff{DiffR06, "GLOBAL", MatchModeInternal, LevelHigh, 1, 0, 1}
		})
}

func TestR07ExpiredIdempotencyDetected(t *testing.T) {
	assertCheck(t, DiffR07, 903601, 903700, time.Date(2026, 10, 21, 0, 0, 0, 0, CST),
		func(t *testing.T) wantDiff {
			for i := 1; i <= 2; i++ {
				execFixture(t,
					`INSERT INTO t_idempotency_record
					   (scope, idem_key, user_id, api_key_id, request_id, state, created_at, expires_at)
					 VALUES ('CLIENT_REQUEST', ?, 903601, 1, ?, 'DONE', ?, ?)`,
					fmt.Sprintf("t21r07-%d-%s", i, randomSuffix(t)),
					rid("R07X", i), time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(-time.Hour))
			}
			return wantDiff{DiffR07, "GLOBAL", MatchModeInternal, LevelMedium, 2, 1, 1}
		})
}

func TestR08CostMarginMismatchDetected(t *testing.T) {
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, CST)
	assertCheck(t, DiffR08, 903701, 903800, day,
		func(t *testing.T) wantDiff {
			execFixture(t,
				`INSERT INTO t_metric_model_daily
				   (stat_date, model_id, request_total, consumed_micro,
				    cost_micro, cost_configured, gross_profit_micro)
				 VALUES (?, 903701, 1, 10000, 3000, 1, 8000)`, day.Format("2006-01-02"))
			return wantDiff{DiffR08, "903701", MatchModeInternal, LevelHigh, 8000, 7000, 1000}
		})
}

// ---- T38: settlement §2 additions ---------------------------------------

// §2-4a: a ledger row whose user has no t_credit_account row. R-01's
// account-side LEFT JOIN cannot see it; the orphan pass must.
func TestR01OrphanLedgerWithoutAccountDetected(t *testing.T) {
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	assertCheck(t, DiffR01, 903901, 903950, day,
		func(t *testing.T) wantDiff {
			insertLedgerEntry(t, 903901, entryNo("T38A", 1), "RECHARGE",
				"CREDIT", 0, 1200, 1200, 0, 1200, "t38a:1", nil)
			var id int64
			if err := testDB.QueryRow(`SELECT id FROM t_ledger_entry WHERE entry_no = ?`,
				entryNo("T38A", 1)).Scan(&id); err != nil {
				t.Fatalf("read T38A entry: %v", err)
			}
			return wantDiff{DiffMissingUpstream, fmt.Sprintf("ledger:%d", id),
				MatchModeInternal, LevelCritical, 1200, 0, 1200}
		})
}

// §2-4b: the account holds, but the entry's request_id has neither a
// t_usage_record nor a t_prehold_hold row. Balance is consistent so only the
// orphan branch can fire.
func TestR01OrphanLedgerWithoutBusinessDocDetected(t *testing.T) {
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	assertCheck(t, DiffR01, 903951, 904000, day,
		func(t *testing.T) wantDiff {
			insertAccount(t, 903951, 1000, 0)
			req := rid("T38B", 1)
			insertLedgerEntry(t, 903951, entryNo("T38B", 1), "RECHARGE",
				"CREDIT", 0, 1000, 1000, 0, 1000, "t38b:1", &req)
			var id int64
			if err := testDB.QueryRow(`SELECT id FROM t_ledger_entry WHERE entry_no = ?`,
				entryNo("T38B", 1)).Scan(&id); err != nil {
				t.Fatalf("read T38B entry: %v", err)
			}
			return wantDiff{DiffMissingUpstream, fmt.Sprintf("ledger:%d", id),
				MatchModeInternal, LevelCritical, 1000, 0, 1000}
		})
}

// §2-3: SUM(t_credit_batch.remaining_micro) != balance_recharge + balance_bonus.
func TestR02BatchSumMismatchDetected(t *testing.T) {
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	assertCheck(t, DiffR02, 904101, 904150, day,
		func(t *testing.T) wantDiff {
			insertAccount(t, 904101, 1000, 0)
			insertBatch(t, 904101, 1000, 700, 300)
			return wantDiff{DiffAmountMismatch, "batch:904101",
				MatchModeInternal, LevelCritical, 1000, 700, 300}
		})
}

// §2-1: the day window is broken while every adjacent pair is continuous.
// Entry 2 claims balance_after=2000 for an amount of 0 on a before of 1000, so
// first_before+SUM=1000 != last_after=2000. R-03's continuity pass alone would
// miss it, which is exactly the gap T38 closes.
func TestR03DayWindowConservationDetected(t *testing.T) {
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	at := day.Add(10 * time.Hour)
	assertCheck(t, DiffR03, 904201, 904250, day,
		func(t *testing.T) wantDiff {
			insertLedgerEntryAt(t, 904201, entryNo("T38C", 1), "RECHARGE", "CREDIT",
				0, 1000, 1000, 0, 1000, "t38c:1", nil, at)
			insertLedgerEntryAt(t, 904201, entryNo("T38C", 2), "ADJUST", "CREDIT",
				0, 0, 0, 1000, 2000, "t38c:2", nil, at)
			return wantDiff{DiffAmountMismatch, "user:904201",
				MatchModeInternal, LevelCritical, 2000, 1000, 1000}
		})
}

// §2-5: SUCCESS + consumed>0, older than one hour, with no CONSUME_SETTLE row.
func TestR06UnsettledSuccessDetected(t *testing.T) {
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	req := rid("T38D", 1)
	assertCheck(t, DiffR06, 904301, 904350, day,
		func(t *testing.T) wantDiff {
			insertUsage(t, req, 904301, day.Add(10*time.Hour), "SUCCESS", 500)
			return wantDiff{DiffMissingUpstream, "unsettled:" + req,
				MatchModeInternal, LevelP0, 0, 500, 500}
		})
}

// §2-8: config_version > 0 with no t_config_publish row of that version.
func TestR09ConfigVersionTraceDetected(t *testing.T) {
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	req := rid("T38E", 1)
	assertCheck(t, checkCodeR09, 904401, 904450, day,
		func(t *testing.T) wantDiff {
			execFixture(t,
				`INSERT INTO t_usage_record
				   (request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
				    request_model_req, inbound_protocol, http_status, result,
				    consumed_micro, total_ms, config_version, created_at)
				 VALUES (?,904401,1,'fixture','fixture-model','fixture-model',
				         'OPENAI_COMPAT',200,'SUCCESS',1,1,990077,?)`,
				req, day.Add(10*time.Hour).UTC())
			return wantDiff{DiffMissingLocal, "config:" + req,
				MatchModeInternal, LevelHigh, 0, 990077, 990077}
		})
}

// T38 / D-31: chain order is (created_at, id). id=3 is a backfilled seed row
// with an earlier created_at; ordering by id alone reports a CRITICAL between
// the live row and the seed, ordering by (created_at, id) does not.
func TestR03OrderingUsesCreatedAtThenID(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 8, 19, 0, 0, 0, 0, CST)
	bizDate := day.Format("2006-01-02")
	dayStart, dayEnd := day, day.AddDate(0, 0, 1)
	const lo, hi = 904501, 904550

	cleanReconScope(t, lo, hi, bizDate)
	t.Cleanup(func() { cleanReconScope(t, lo, hi, bizDate) })

	live := day.Add(10 * time.Hour)
	// Live rows first (lower ids), seed row after (higher id, older created_at).
	insertLedgerEntryAt(t, 904501, entryNo("T38F", 1), "CONSUME_PREHOLD", "DEBIT",
		0, -200, -200, 1000, 800, "t38f:1", nil, live)
	insertLedgerEntryAt(t, 904501, entryNo("T38F", 2), "PREHOLD_RELEASE", "CREDIT",
		0, 200, 200, 800, 1000, "t38f:2", nil, live)
	insertLedgerEntryAt(t, 904501, entryNo("T38F", 3), "RECHARGE", "CREDIT",
		0, 1000, 1000, 0, 1000, "t38f:3", nil, day.AddDate(0, 0, -30))

	d := NewDaily(testDB, nil, nil, nil, "t38-order")
	d.UserLo, d.UserHi = lo, hi
	check, ok := checkByCode(d, DiffR03)
	if !ok {
		t.Fatal("R-03 not registered")
	}
	diffs, err := check.Run(ctx, d, bizDate, dayStart, dayEnd)
	if err != nil {
		t.Fatalf("R-03: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("R-03 reported %d diff(s) for a (created_at, id)-ordered chain: %+v", len(diffs), diffs)
	}
}
