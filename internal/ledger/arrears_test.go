package ledger

import (
	"testing"
)

// A5: arrears 100, balance -500, recharge 200. The recharge repays the debt
// first; the balance rises by 100 (from -500 to -400) and the account is
// unfrozen. The card writes "余额 +100", which is the delta, not a positive
// balance.
func TestA5RechargeOffsetsArrearsFirst(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	newAccount(t, userID, 0, -500, 500)
	setUserStatus(t, userID, UserStatusFrozenOverdraft)
	requestID := newRequestID("a5")
	insertArrearsDirect(t, userID, requestID, 100, ArrearsStateOpen)

	res, err := s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 200, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("Recharge: %v", err)
	}
	if res.OffsetMicro != 100 {
		t.Fatalf("offset = %d, want 100", res.OffsetMicro)
	}
	if res.RepairMicro != 100 {
		t.Fatalf("negative-balance repair = %d, want 100", res.RepairMicro)
	}
	if res.RemainingArrearsMicro != 0 {
		t.Fatalf("remaining arrears = %d, want 0", res.RemainingArrearsMicro)
	}
	st := getAccount(t, userID)
	if st.Total != -400 {
		t.Fatalf("balance = %d, want -400 (delta +100 from -500)", st.Total)
	}
	if delta := st.Total - (-500); delta != 100 {
		t.Fatalf("balance delta = %d, want +100", delta)
	}
	if arrears, ok := getArrears(t, requestID); !ok || arrears.State != ArrearsStateRepaid || arrears.Settled != 100 {
		t.Fatalf("arrears = %+v (exists=%v), want REPAID/100", arrears, ok)
	}
	if status := getUserStatus(t, userID); status != UserStatusNormal {
		t.Fatalf("status = %s, want NORMAL", status)
	}

	// Batch invariant: no positive batch is left behind while the account is
	// negative; all 200 credits were absorbed by arrears + the deficit.
	var remaining int64
	if err := testDB.QueryRow(
		`SELECT COALESCE(SUM(remaining_micro),0) FROM t_credit_batch WHERE user_id=?`,
		userID,
	).Scan(&remaining); err != nil {
		t.Fatalf("sum batch remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("batch remaining = %d, want 0 (balance is still negative)", remaining)
	}
	t.Logf("A5 recharge=200: offset=%d repair=%d balance=-500 -> %d (delta +%d) arrears=%d status=%s",
		res.OffsetMicro, res.RepairMicro, st.Total, st.Total+500, res.RemainingArrearsMicro, getUserStatus(t, userID))
}

// The card's literal end state "余额 +100" is reached when the recharge is
// 700: 100 to arrears, 500 to repair the -500 deficit, 100 left available.
func TestA5RechargeRepairsOverdraftThenCredits(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	newAccount(t, userID, 0, -500, 500)
	setUserStatus(t, userID, UserStatusFrozenOverdraft)
	requestID := newRequestID("a5b")
	insertArrearsDirect(t, userID, requestID, 100, ArrearsStateOpen)

	res, err := s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 700, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("Recharge: %v", err)
	}
	if res.OffsetMicro != 100 || res.RepairMicro != 500 {
		t.Fatalf("offset/repair = %d/%d, want 100/500", res.OffsetMicro, res.RepairMicro)
	}
	if st := getAccount(t, userID); st.Total != 100 {
		t.Fatalf("balance = %d, want 100", st.Total)
	}
	var remaining int64
	if err := testDB.QueryRow(
		`SELECT COALESCE(SUM(remaining_micro),0) FROM t_credit_batch WHERE user_id=?`,
		userID,
	).Scan(&remaining); err != nil {
		t.Fatalf("sum batch remaining: %v", err)
	}
	if remaining != 100 {
		t.Fatalf("batch remaining = %d, want 100 (matches positive balance)", remaining)
	}
	if status := getUserStatus(t, userID); status != UserStatusNormal {
		t.Fatalf("status = %s, want NORMAL", status)
	}
}

func TestRechargeIsIdempotentByOrder(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 1000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 1000, Remaining: 1000})

	p := RechargeParams{UserID: userID, AmountMicro: 500, RechargeOrderID: uniqueOrderID()}
	first, err := s.Recharge(t.Context(), p)
	if err != nil {
		t.Fatalf("first Recharge: %v", err)
	}
	second, err := s.Recharge(t.Context(), p)
	if err != nil {
		t.Fatalf("second Recharge: %v", err)
	}
	if !second.Idempotent {
		t.Fatal("second recharge must be idempotent")
	}
	if second.BalanceAfterMicro != first.BalanceAfterMicro {
		t.Fatalf("balance changed on replay: %d -> %d", first.BalanceAfterMicro, second.BalanceAfterMicro)
	}
	if st := getAccount(t, userID); st.Total != 1500 {
		t.Fatalf("balance = %d, want 1500", st.Total)
	}
	if entries := ledgerEntries(t, userID, BizTypeRecharge); len(entries) != 1 {
		t.Fatalf("recharge entries = %d, want 1", len(entries))
	}
}

func TestRechargePositiveBalanceDoesNotRepair(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 1000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 1000, Remaining: 1000})

	res, err := s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 500, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("Recharge: %v", err)
	}
	if res.OffsetMicro != 0 || res.RepairMicro != 0 {
		t.Fatalf("offset/repair = %d/%d, want 0/0", res.OffsetMicro, res.RepairMicro)
	}
	if st := getAccount(t, userID); st.Total != 1500 {
		t.Fatalf("balance = %d, want 1500", st.Total)
	}
	var remaining int64
	if err := testDB.QueryRow(
		`SELECT COALESCE(SUM(remaining_micro),0) FROM t_credit_batch WHERE user_id=?`,
		userID,
	).Scan(&remaining); err != nil {
		t.Fatalf("sum batch remaining: %v", err)
	}
	if remaining != 1500 {
		t.Fatalf("batch remaining = %d, want 1500", remaining)
	}
}

func TestArrearsPartialThenRepaid(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	newAccount(t, userID, 0, -500, 500)
	setUserStatus(t, userID, UserStatusFrozenOverdraft)
	requestID := newRequestID("partial")
	insertArrearsDirect(t, userID, requestID, 1000, ArrearsStateOpen)

	res, err := s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 400, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("first Recharge: %v", err)
	}
	if res.OffsetMicro != 400 || res.RemainingArrearsMicro != 600 {
		t.Fatalf("offset/remaining = %d/%d, want 400/600", res.OffsetMicro, res.RemainingArrearsMicro)
	}
	if arrears, _ := getArrears(t, requestID); arrears.State != ArrearsStatePartial || arrears.Settled != 400 {
		t.Fatalf("arrears after partial = %+v, want PARTIAL/400", arrears)
	}
	if status := getUserStatus(t, userID); status != UserStatusFrozenOverdraft {
		t.Fatalf("status = %s, want still FROZEN_OVERDRAFT", status)
	}

	res, err = s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 600, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("second Recharge: %v", err)
	}
	if res.OffsetMicro != 600 || res.RemainingArrearsMicro != 0 {
		t.Fatalf("offset/remaining = %d/%d, want 600/0", res.OffsetMicro, res.RemainingArrearsMicro)
	}
	if arrears, _ := getArrears(t, requestID); arrears.State != ArrearsStateRepaid || arrears.Settled != 1000 {
		t.Fatalf("arrears after full = %+v, want REPAID/1000", arrears)
	}
	if status := getUserStatus(t, userID); status != UserStatusNormal {
		t.Fatalf("status = %s, want NORMAL", status)
	}
}
