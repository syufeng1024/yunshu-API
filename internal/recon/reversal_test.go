package recon

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"yunshu.dev/core/internal/platform/clock"
)

type ledgerSnapshot struct {
	Direction, BizType string
	Bonus, Recharge    int64
	Total, Before      int64
	After              int64
	IdemKey            string
	Operator           sql.NullInt64
	Reason             sql.NullString
}

func readEntry(t *testing.T, entryNo string) ledgerSnapshot {
	t.Helper()
	var s ledgerSnapshot
	if err := testDB.QueryRow(
		`SELECT direction, biz_type, amount_bonus_micro, amount_recharge_micro,
		        amount_total_micro, balance_before_micro, balance_after_micro,
		        idem_key, operator_id, reason
		   FROM t_ledger_entry WHERE entry_no = ?`, entryNo,
	).Scan(&s.Direction, &s.BizType, &s.Bonus, &s.Recharge, &s.Total,
		&s.Before, &s.After, &s.IdemKey, &s.Operator, &s.Reason); err != nil {
		t.Fatalf("read entry %s: %v", entryNo, err)
	}
	return s
}

// A9: reversal is append-only. The original row is byte-for-byte unchanged, the
// new entry negates it, and the two sum to zero.
func TestReversalAppendsOppositeEntry(t *testing.T) {
	ctx := context.Background()
	const user = int64(904001)
	origNo := entryNo("REV", 1)
	deleteAccount(t, user)
	insertAccount(t, user, 700000, 0)
	insertLedgerEntry(t, user, entryNo("REV", 0), "RECHARGE", "CREDIT",
		0, 1000000, 1000000, 0, 1000000, "rev:open", nil)
	insertLedgerEntry(t, user, origNo, "CONSUME_SETTLE", "DEBIT",
		0, -300000, -300000, 1000000, 700000, "settle:rev-orig", nil)
	t.Cleanup(func() {
		deleteAccount(t, user)
		_, _ = testDB.Exec(`DELETE FROM t_notification WHERE event_code IN ('RECONCILE_ALERT','RECONCILE_MANUAL')`)
	})

	before := readEntry(t, origNo)
	rev := NewReverser(testDB, clock.Real{})
	res, err := rev.Reverse(ctx, ReverseParams{
		EntryNo: origNo, Reason: "对账发现重复扣费需冲正", OperatorID: 1001,
	})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if res.Idempotent || res.AmountTotalMicro != 300000 ||
		res.BalanceBefore != 700000 || res.BalanceAfter != 1000000 {
		t.Fatalf("reverse result = %+v; want +300000 700000->1000000", res)
	}

	after := readEntry(t, origNo)
	if after != before {
		t.Fatalf("original entry changed:\nbefore=%+v\nafter =%+v", before, after)
	}

	reversal := readEntry(t, res.EntryNo)
	if reversal.BizType != "ADJUST" || reversal.Direction != "CREDIT" ||
		reversal.Total != 300000 || reversal.Before != 700000 || reversal.After != 1000000 {
		t.Fatalf("reversal entry = %+v; want ADJUST CREDIT +300000 700000->1000000", reversal)
	}
	if reversal.IdemKey != "reversal:"+origNo {
		t.Fatalf("reversal idem_key = %q; want reversal:%s", reversal.IdemKey, origNo)
	}
	if !reversal.Operator.Valid || reversal.Operator.Int64 != 1001 {
		t.Fatalf("reversal operator = %+v; want 1001", reversal.Operator)
	}
	if !strings.HasPrefix(reversal.Reason.String, ReversalReasonPrefix+origNo+":") {
		t.Fatalf("reversal reason = %q; want prefix %s%s:", reversal.Reason.String, ReversalReasonPrefix, origNo)
	}

	var pairSum, sum, rows int64
	if err := testDB.QueryRow(
		`SELECT COALESCE(SUM(amount_total_micro),0)
		   FROM t_ledger_entry WHERE idem_key IN ('settle:rev-orig', ?)`,
		"reversal:"+origNo,
	).Scan(&pairSum); err != nil {
		t.Fatalf("sum pair: %v", err)
	}
	if err := testDB.QueryRow(
		`SELECT COALESCE(SUM(amount_total_micro),0), COUNT(*) FROM t_ledger_entry WHERE user_id = ?`, user,
	).Scan(&sum, &rows); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	if pairSum != 0 {
		t.Fatalf("original+reversal sum = %d; want 0", pairSum)
	}
	if rows != 3 {
		t.Fatalf("ledger rows = %d; want 3", rows)
	}
	var balance int64
	if err := testDB.QueryRow(
		`SELECT balance_micro FROM t_credit_account WHERE user_id = ?`, user).Scan(&balance); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if balance != 1000000 || sum != balance {
		t.Fatalf("account balance=%d ledger sum=%d; want 1000000", balance, sum)
	}

	// Second call is idempotent: same row, no second reversal.
	again, err := rev.Reverse(ctx, ReverseParams{
		EntryNo: origNo, Reason: "对账发现重复扣费需冲正", OperatorID: 1001,
	})
	if err != nil {
		t.Fatalf("second reverse: %v", err)
	}
	if !again.Idempotent || again.ID != res.ID {
		t.Fatalf("second reverse = %+v; want idempotent id=%d", again, res.ID)
	}
	var count int64
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM t_ledger_entry WHERE user_id = ?`, user).Scan(&count); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 3 {
		t.Fatalf("ledger rows after replay = %d; want 2", count)
	}

	// The 10-character reason floor is enforced before any write.
	if _, err := rev.Reverse(ctx, ReverseParams{
		EntryNo: origNo, Reason: "太短", OperatorID: 1001,
	}); err == nil {
		t.Fatal("short reason was accepted")
	}
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM t_ledger_entry WHERE user_id = ?`, user).Scan(&count); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 3 {
		t.Fatalf("short reason wrote a row: count=%d", count)
	}
	t.Logf("A9 original=%s unchanged=%t reversal=%s amount=%d sum=%d rows=%d",
		origNo, after == before, res.EntryNo, res.AmountTotalMicro, sum, rows)
}
