package ledger

// T21 deliverable 8: the ledger invariants that reconciliation assumes.
//
// These run against the real schema. A healthy account must satisfy all four:
//
//	SUM(t_ledger_entry.amount_total_micro) == t_credit_account.balance_micro
//	balance_micro == balance_recharge_micro + balance_bonus_micro
//	t_credit_batch.remaining_micro == face_value_micro - consumed_micro
//	every ledger row references an existing account (and batch, when set)
//
// Two negative controls prove the checks can fail: a planted balance drift and
// a planted orphan row are both detected, so a green run is not a tautology.

import (
	"fmt"
	"testing"
	"time"
)

type invariantViolation struct {
	Kind   string
	Detail string
}

func checkInvariants(t *testing.T, uid int64) []invariantViolation {
	t.Helper()
	var out []invariantViolation

	var ledgerSum, balance, recharge, bonus int64
	if err := testDB.QueryRow(`
		SELECT COALESCE(SUM(l.amount_total_micro), 0)
		  FROM t_ledger_entry l WHERE l.user_id = ?`, uid).Scan(&ledgerSum); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	if err := testDB.QueryRow(`
		SELECT balance_micro, balance_recharge_micro, balance_bonus_micro
		  FROM t_credit_account WHERE user_id = ?`, uid).Scan(&balance, &recharge, &bonus); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if ledgerSum != balance {
		out = append(out, invariantViolation{"ledger_sum", fmt.Sprintf("sum=%d balance=%d", ledgerSum, balance)})
	}
	if balance != recharge+bonus {
		out = append(out, invariantViolation{"balance_split", fmt.Sprintf("balance=%d recharge=%d bonus=%d", balance, recharge, bonus)})
	}

	rows, err := testDB.Query(`
		SELECT id, face_value_micro, remaining_micro, consumed_micro
		  FROM t_credit_batch WHERE user_id = ?`, uid)
	if err != nil {
		t.Fatalf("read batches: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, face, remaining, consumed int64
		if err := rows.Scan(&id, &face, &remaining, &consumed); err != nil {
			t.Fatalf("scan batch: %v", err)
		}
		if remaining != face-consumed {
			out = append(out, invariantViolation{"batch",
				fmt.Sprintf("batch %d remaining=%d face=%d consumed=%d", id, remaining, face, consumed)})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate batches: %v", err)
	}

	var orphans int64
	if err := testDB.QueryRow(`
		SELECT COUNT(*) FROM t_ledger_entry l
		 LEFT JOIN t_credit_account a ON a.user_id = l.user_id
		 WHERE l.user_id = ? AND a.user_id IS NULL`, uid).Scan(&orphans); err != nil {
		t.Fatalf("account orphans: %v", err)
	}
	if orphans != 0 {
		out = append(out, invariantViolation{"orphan_account", fmt.Sprintf("%d entries without an account", orphans)})
	}
	if err := testDB.QueryRow(`
		SELECT COUNT(*) FROM t_ledger_entry l
		 LEFT JOIN t_credit_batch b ON b.id = l.batch_id
		 WHERE l.user_id = ? AND l.batch_id IS NOT NULL AND b.id IS NULL`, uid).Scan(&orphans); err != nil {
		t.Fatalf("batch orphans: %v", err)
	}
	if orphans != 0 {
		out = append(out, invariantViolation{"orphan_batch", fmt.Sprintf("%d entries reference a missing batch", orphans)})
	}
	return out
}

func requireHealthy(t *testing.T, uid int64) {
	t.Helper()
	if v := checkInvariants(t, uid); len(v) != 0 {
		t.Fatalf("ledger invariants violated for user %d: %+v", uid, v)
	}
}

// TestInvariantGrantPreholdSettle walks one request through the normal path and
// checks the four invariants after each money-moving step.
func TestInvariantGrantPreholdSettle(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, 500000)
	newAccount(t, uid, 0, 0, 500000)

	res, err := s.Grant(t.Context(), GrantParams{
		UserID:          uid,
		RechargeMicro:   100000,
		BizType:         BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("t21-inv-grant:%d", uid),
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if res.BalanceAfterMicro != 100000 {
		t.Fatalf("balance after grant = %d, want 100000", res.BalanceAfterMicro)
	}
	requireHealthy(t, uid)

	requestID := newRequestID("t21inv")
	preholdFor(t, s, uid, 6000, 7200, requestID)
	requireHealthy(t, uid)

	if _, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: uid, ConsumedMicro: 5400,
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	requireHealthy(t, uid)

	if st := getAccount(t, uid); st.Total != 100000-5400 {
		t.Fatalf("balance = %d, want %d", st.Total, 100000-5400)
	}
	// The prehold row is now settled and the batch carries the debit.
	var remaining, consumed int64
	if err := testDB.QueryRow(`
		SELECT remaining_micro, consumed_micro FROM t_credit_batch WHERE user_id = ?`, uid).
		Scan(&remaining, &consumed); err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if remaining != 94600 || consumed != 5400 {
		t.Fatalf("batch remaining/consumed = %d/%d, want 94600/5400", remaining, consumed)
	}
}

// TestInvariantDetectsBalanceDrift plants a one-micro drift; the checker must
// report it. The row is restored so the shared schema is left clean.
func TestInvariantDetectsBalanceDrift(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, 0)
	newAccount(t, uid, 0, 0, 0)
	if _, err := s.Grant(t.Context(), GrantParams{
		UserID: uid, RechargeMicro: 1000, BizType: BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("t21-inv-drift:%d", uid),
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	requireHealthy(t, uid)

	if _, err := testDB.Exec(`
		UPDATE t_credit_account SET balance_micro = balance_micro + 1 WHERE user_id = ?`, uid); err != nil {
		t.Fatalf("plant drift: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.Exec(`UPDATE t_credit_account SET balance_micro = balance_micro - 1 WHERE user_id = ?`, uid)
	})
	if v := checkInvariants(t, uid); len(v) == 0 {
		t.Fatal("balance drift was not detected")
	} else {
		t.Logf("detected drift: %+v", v)
	}
}

// TestInvariantDetectsOrphanEntry plants a ledger row whose batch does not
// exist. The ledger has no foreign key, so only this check catches it.
func TestInvariantDetectsOrphanEntry(t *testing.T) {
	s := newTestStore(t)
	uid := newUser(t, 0)
	newAccount(t, uid, 0, 0, 0)
	if _, err := s.Grant(t.Context(), GrantParams{
		UserID: uid, RechargeMicro: 1000, BizType: BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("t21-inv-orphan:%d", uid),
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	now := time.Now().UTC()
	missingBatch := int64(9_999_999_999)
	if _, err := testDB.Exec(`
		INSERT INTO t_ledger_entry
			(user_id, entry_no, account_type, direction, biz_type,
			 amount_bonus_micro, amount_recharge_micro, amount_total_micro,
			 balance_before_micro, balance_after_micro, batch_id, idem_key, created_at)
		VALUES (?,?,'USER','CREDIT','RECHARGE',0,0,0,0,0,?,?,?)`,
		uid, NewEntryNoGenerator().Next(now), missingBatch, fmt.Sprintf("t21-inv-orphan-entry:%d", uid), now); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}
	v := checkInvariants(t, uid)
	found := false
	for _, item := range v {
		if item.Kind == "orphan_batch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan batch was not detected: %+v", v)
	}
	t.Logf("detected orphan: %+v", v)
}
