package fund

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// T30 / G12: buying a plan must never erase an approved quota increase.
//
// The runtime resolves one USER policy row with
//   effective_from <= now AND (effective_to IS NULL OR effective_to > now)
// ordered by effective_from DESC, id DESC.
// These helpers read the table through exactly that predicate so the tests
// cannot pass on a query that differs from the gateway's.

type rlpRow struct {
	ID     int64
	Source string
	RPM    int64
	TPM    int64
	Conc   int64
	Daily  int64
	From   time.Time
	To     sql.NullTime
}

const rlpColumns = `id, source, rpm, tpm, concurrency_quota, daily_quota_micro,
	effective_from, effective_to`

func scanRLP(row interface{ Scan(...any) error }) (rlpRow, error) {
	var r rlpRow
	err := row.Scan(&r.ID, &r.Source, &r.RPM, &r.TPM, &r.Conc, &r.Daily, &r.From, &r.To)
	return r, err
}

// policyByID reads one row back exactly as stored (all comparisons are on the
// persisted values, so MySQL's millisecond truncation cannot make them flaky).
func policyByID(t *testing.T, id int64) rlpRow {
	t.Helper()
	r, err := scanRLP(testDB.QueryRowContext(context.Background(),
		`SELECT `+rlpColumns+` FROM t_rate_limit_policy WHERE id = ?`, id))
	if err != nil {
		t.Fatalf("policy %d: %v", id, err)
	}
	return r
}

func latestPolicyOfSource(t *testing.T, uid int64, source string) rlpRow {
	t.Helper()
	r, err := scanRLP(testDB.QueryRowContext(context.Background(),
		`SELECT `+rlpColumns+` FROM t_rate_limit_policy
		  WHERE scope_type = 'USER' AND scope_id = ? AND source = ?
		  ORDER BY effective_from DESC, id DESC LIMIT 1`, uid, source))
	if err != nil {
		t.Fatalf("latest %s policy: %v", source, err)
	}
	return r
}

// activePolicyRuntime is the runtime口径 lookup, verbatim.
func activePolicyRuntime(t *testing.T, uid int64) rlpRow {
	t.Helper()
	now := time.Now().UTC()
	r, err := scanRLP(testDB.QueryRowContext(context.Background(),
		`SELECT `+rlpColumns+` FROM t_rate_limit_policy
		  WHERE scope_type = 'USER' AND scope_id = ?
		    AND effective_from <= ?
		    AND (effective_to IS NULL OR effective_to > ?)
		  ORDER BY effective_from DESC, id DESC LIMIT 1`, uid, now, now))
	if err != nil {
		t.Fatalf("active policy: %v", err)
	}
	return r
}

// awaitPolicyStart waits until the runtime query can see a row that starts at
// from. A purchase anchors its rows at the payment instant and the quota
// continuation starts 1ms later (D-27 #3); reading back in the same
// millisecond would otherwise hit the preceding plan row.
func awaitPolicyStart(from time.Time) {
	if d := time.Until(from) + 2*time.Millisecond; d > 0 {
		time.Sleep(d)
	}
}

// seedQuotaPolicy inserts one QUOTA_APPLY row directly; tests own the
// timeline, so the effective window is written as given.
func seedQuotaPolicy(t *testing.T, uid, rpm, tpm, conc, daily int64, from, to time.Time) int64 {
	t.Helper()
	now := time.Now().UTC()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_rate_limit_policy
			(scope_type, scope_id, source, rpm, tpm, concurrency_quota,
			 daily_quota_micro, effective_from, effective_to,
			 fallback_policy_id, created_at, updated_at)
		VALUES ('USER', ?, 'QUOTA_APPLY', ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		uid, rpm, tpm, conc, daily, from.Truncate(time.Millisecond),
		to.Truncate(time.Millisecond), now, now)
	if err != nil {
		t.Fatalf("seed quota policy: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed quota policy id: %v", err)
	}
	return id
}

// T1: a QUOTA_APPLY row that starts tomorrow keeps every field — the close
// statement must only touch rows already in force (D-27 #1).
func TestT30FutureQuotaRowUntouchedByPurchase(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)

	now := time.Now().UTC()
	from := now.Add(24 * time.Hour)
	to := from.AddDate(0, 0, 30)
	const daily = int64(50_000) * MicroPerCredit
	id := seedQuotaPolicy(t, uid, 900, 900_000, 90, daily, from, to)

	before := policyByID(t, id)
	if !before.To.Valid || !before.To.Time.After(before.From) {
		t.Fatalf("seeded window is not positive: from=%s to=%v", before.From, before.To)
	}

	payPlanOrder(t, svc, uid, "STANDARD")

	after := policyByID(t, id)
	if after.ID != before.ID {
		t.Fatalf("future row id changed %d -> %d (row was recreated)", before.ID, after.ID)
	}
	if !after.From.Equal(before.From) {
		t.Fatalf("future row effective_from moved: %s -> %s", before.From, after.From)
	}
	if after.To.Valid != before.To.Valid || !after.To.Time.Equal(before.To.Time) {
		t.Fatalf("future row effective_to moved: %v -> %v", before.To, after.To)
	}
	if after.Daily != before.Daily {
		t.Fatalf("future row daily quota moved: %d -> %d", before.Daily, after.Daily)
	}
	if !after.To.Time.After(after.From) {
		t.Fatalf("future row window became non-positive: from=%s to=%s", after.From, after.To.Time)
	}

	// Exactly one row at that instant: the fix did not delete-and-rebuild.
	var n int
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ? AND effective_from = ?`,
		uid, before.From).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows at the future instant = %d, want 1", n)
	}
	t.Logf("T1: future row id=%d kept from=%s to=%s daily=%d", after.ID, after.From, after.To.Time, after.Daily)
}

// T2: right after a purchase the runtime lookup returns the new plan row with
// the purchased specs (D-27 #2: effective_to stays NULL).
func TestT30PlanActiveRightAfterPurchase(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	pro := planByCode(t, "PRO")

	payPlanOrder(t, svc, uid, "PRO")

	plan := latestPolicyOfSource(t, uid, "PLAN")
	awaitPolicyStart(plan.From)
	active := activePolicyRuntime(t, uid)
	if active.Source != "PLAN" || active.ID != plan.ID {
		t.Fatalf("active row = %s/%d, want PLAN/%d", active.Source, active.ID, plan.ID)
	}
	if active.RPM != pro.RPM || active.TPM != pro.TPM || active.Conc != pro.Concurrency {
		t.Fatalf("active specs = %d/%d/%d, want %d/%d/%d",
			active.RPM, active.TPM, active.Conc, pro.RPM, pro.TPM, pro.Concurrency)
	}
	if pro.DailyQuotaMicro == nil || active.Daily != *pro.DailyQuotaMicro {
		t.Fatalf("active daily = %d, want %v", active.Daily, pro.DailyQuotaMicro)
	}
	if active.To.Valid {
		t.Fatalf("paid plan effective_to = %v, want NULL (permanent)", active.To.Time)
	}
	t.Logf("T2: active PLAN row id=%d rpm/tpm/conc=%d/%d/%d daily=%d",
		active.ID, active.RPM, active.TPM, active.Conc, active.Daily)
}

// T3: an already-running QUOTA_APPLY row keeps its daily quota across a
// purchase, takes the new plan's three specs, keeps its window end, and is
// truncated at the purchase instant as audit history (D-27 #3).
func TestT30ActiveQuotaSurvivesPurchase(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	pro := planByCode(t, "PRO")

	now := time.Now().UTC()
	quotaFrom := now.Add(-time.Hour)
	quotaTo := now.AddDate(0, 0, 29)
	const daily = int64(50_000) * MicroPerCredit
	qid := seedQuotaPolicy(t, uid, 7, 7_000, 7, daily, quotaFrom, quotaTo)

	start := time.Now().UTC()
	payPlanOrder(t, svc, uid, "PRO")
	end := time.Now().UTC()

	// The original row is history now: window truncated to the purchase
	// instant, specs and quota unchanged.
	orig := policyByID(t, qid)
	if !orig.To.Valid {
		t.Fatalf("original quota row effective_to is NULL, want truncated")
	}
	if orig.To.Time.Before(start) || orig.To.Time.After(end) {
		t.Fatalf("original quota row truncated to %s, want within purchase window [%s, %s]",
			orig.To.Time, start, end)
	}
	if !orig.To.Time.Before(quotaTo) {
		t.Fatalf("original quota row was not truncated: to=%s, original end=%s", orig.To.Time, quotaTo)
	}
	if orig.Daily != daily || orig.RPM != 7 || orig.TPM != 7_000 || orig.Conc != 7 {
		t.Fatalf("original quota row mutated: daily=%d rpm/tpm/conc=%d/%d/%d, want %d/7/7000/7",
			orig.Daily, orig.RPM, orig.TPM, orig.Conc, daily)
	}

	// A continuation row carries quota + new specs over the remaining window.
	cont := latestPolicyOfSource(t, uid, "QUOTA_APPLY")
	if cont.ID == qid {
		t.Fatalf("no continuation row: latest QUOTA_APPLY is still the truncated row %d", qid)
	}
	if cont.Daily != daily {
		t.Fatalf("continuation daily = %d, want the approved %d", cont.Daily, daily)
	}
	if cont.RPM != pro.RPM || cont.TPM != pro.TPM || cont.Conc != pro.Concurrency {
		t.Fatalf("continuation specs = %d/%d/%d, want PRO %d/%d/%d",
			cont.RPM, cont.TPM, cont.Conc, pro.RPM, pro.TPM, pro.Concurrency)
	}
	if !cont.To.Valid || !cont.To.Time.Equal(quotaTo.Truncate(time.Millisecond)) {
		t.Fatalf("continuation effective_to = %v, want the original %s", cont.To, quotaTo)
	}
	plan := latestPolicyOfSource(t, uid, "PLAN")
	if !cont.From.After(plan.From) {
		t.Fatalf("continuation starts %s, not after the plan row %s", cont.From, plan.From)
	}

	awaitPolicyStart(cont.From)
	active := activePolicyRuntime(t, uid)
	if active.ID != cont.ID {
		t.Fatalf("active row id=%d (%s), want the continuation %d", active.ID, active.Source, cont.ID)
	}
	if active.Daily != daily || active.RPM != pro.RPM {
		t.Fatalf("active = daily %d rpm %d, want daily %d rpm %d",
			active.Daily, active.RPM, daily, pro.RPM)
	}
	t.Logf("T3: original %d truncated at %s; continuation %d from=%s to=%s daily=%d specs=%d/%d/%d",
		qid, orig.To.Time, cont.ID, cont.From, cont.To.Time, cont.Daily, cont.RPM, cont.TPM, cont.Conc)
}

// T4: once the quota window is over, the purchased plan row is the only
// active row again — no delayed job or re-open is needed (D-27 #2).
func TestT30PlanResumesAfterQuotaWindowEnds(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	standard := planByCode(t, "STANDARD")

	now := time.Now().UTC()
	seedQuotaPolicy(t, uid, 900, 900_000, 90, int64(50_000)*MicroPerCredit,
		now.Add(-48*time.Hour), now.Add(-24*time.Hour))

	payPlanOrder(t, svc, uid, "STANDARD")

	plan := latestPolicyOfSource(t, uid, "PLAN")
	awaitPolicyStart(plan.From)
	active := activePolicyRuntime(t, uid)
	if active.Source != "PLAN" || active.ID != plan.ID {
		t.Fatalf("active row = %s/%d, want the purchased PLAN/%d", active.Source, active.ID, plan.ID)
	}
	if active.RPM != standard.RPM || active.TPM != standard.TPM || active.Conc != standard.Concurrency {
		t.Fatalf("active specs = %d/%d/%d, want STANDARD %d/%d/%d",
			active.RPM, active.TPM, active.Conc, standard.RPM, standard.TPM, standard.Concurrency)
	}
	if active.To.Valid {
		t.Fatalf("plan row effective_to = %v, want NULL", active.To.Time)
	}
	t.Logf("T4: ended quota window gives way to PLAN row id=%d specs=%d/%d/%d",
		active.ID, active.RPM, active.TPM, active.Conc)
}

// T5: A8/A9 (plan specs merge, repeat purchase) are covered by
// TestPlanUpgradeOverridesSpecs and TestRepeatPurchaseSameTier; they must stay
// green alongside these cases.
