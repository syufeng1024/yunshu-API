package ledger

import (
	"errors"
	"testing"
	"time"
)

func preholdFor(t *testing.T, s *Store, userID, raw, est int64, requestID string) PreholdResult {
	t.Helper()
	pre, err := s.Prehold(t.Context(), PreholdParams{
		RequestID: requestID, UserID: userID, APIKeyID: 1, ModelID: 1, NodeID: "test",
		RawEstMicro: raw, EstMicro: est, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Prehold: %v", err)
	}
	return pre
}

// A4 / G-02: balance 100 + overdraft 500, actual 600 => balance stops at
// -500, no arrears (100 + 500 exactly covers the cost).
func TestA4TruncationExactlyAtCap(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	newAccount(t, userID, 0, 100, 500)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 100, Remaining: 100})

	requestID := newRequestID("trunc600")
	preholdFor(t, s, userID, 100, 100, requestID)

	res, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 600,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.ConsumedMicro != 600 || res.ArrearsMicro != 0 {
		t.Fatalf("result = %+v, want consumed 600 / arrears 0", res)
	}
	if res.BatchBackedMicro != 100 || res.OverdraftMicro != 500 {
		t.Fatalf("batch/overdraft = %d/%d, want 100/500", res.BatchBackedMicro, res.OverdraftMicro)
	}
	st := getAccount(t, userID)
	if st.Total != -500 {
		t.Fatalf("balance = %d, want -500", st.Total)
	}
	if _, ok := getArrears(t, requestID); ok {
		t.Fatal("arrears row must not exist when the cap exactly covers the cost")
	}
	if status := getUserStatus(t, userID); status != UserStatusNormal {
		t.Fatalf("status = %s, want NORMAL (no arrears)", status)
	}
	t.Logf("A4 100/600: balance=%d arrears=0 status=%s batch_backed=%d overdraft=%d",
		st.Total, getUserStatus(t, userID), res.BatchBackedMicro, res.OverdraftMicro)
}

// A4: balance 100 + overdraft 500, actual 700 => balance -500, arrears 100,
// status FROZEN_OVERDRAFT.
func TestA4TruncationWithArrearsAndFreeze(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	newAccount(t, userID, 0, 100, 500)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 100, Remaining: 100})

	requestID := newRequestID("trunc700")
	preholdFor(t, s, userID, 100, 100, requestID)

	res, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 700,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.ArrearsMicro != 100 {
		t.Fatalf("arrears = %d, want 100", res.ArrearsMicro)
	}
	st := getAccount(t, userID)
	if st.Total != -500 {
		t.Fatalf("balance = %d, want -500 (the floor)", st.Total)
	}
	arrears, ok := getArrears(t, requestID)
	if !ok {
		t.Fatal("arrears row missing")
	}
	if arrears.Amount != 100 || arrears.State != ArrearsStateOpen {
		t.Fatalf("arrears = %+v, want amount 100 / OPEN", arrears)
	}
	if status := getUserStatus(t, userID); status != UserStatusFrozenOverdraft {
		t.Fatalf("status = %s, want FROZEN_OVERDRAFT", status)
	}
	// Money conservation: balance deduction (600) + arrears (100) = 700.
	deducted := 100 - st.Total
	if deducted+arrears.Amount != 700 {
		t.Fatalf("deducted %d + arrears %d != 700", deducted, arrears.Amount)
	}
	t.Logf("A4 100/700: balance=%d arrears=%d status=%s deducted=%d (conservation %d+%d=700)",
		st.Total, arrears.Amount, getUserStatus(t, userID), deducted, deducted, arrears.Amount)
}

// G-02 full truncation example: 400,000 micro balance, 2,000,000 cost,
// 500,000 overdraft => deduct 900,000, arrears 1,100,000, then recharge
// 2,000,000 => offset 1,100,000, balance 400,000, unfrozen.
func TestG02HardTruncationAndRechargeRecovery(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 400000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 400000, Remaining: 400000})

	requestID := newRequestID("g02trunc")
	preholdFor(t, s, userID, 400000, 400000, requestID)

	res, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 2000000,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.ConsumedMicro != 900000 {
		t.Fatalf("deducted = %d, want 900000 (400000 + 500000)", res.ConsumedMicro)
	}
	if res.ArrearsMicro != 1100000 {
		t.Fatalf("arrears = %d, want 1100000", res.ArrearsMicro)
	}
	if st := getAccount(t, userID); st.Total != -500000 {
		t.Fatalf("balance = %d, want -500000", st.Total)
	}
	if status := getUserStatus(t, userID); status != UserStatusFrozenOverdraft {
		t.Fatalf("status = %s, want FROZEN_OVERDRAFT", status)
	}

	// Freeze means no further request may be admitted, even with a tiny
	// estimate: the overdraft limit is not a new admission allowance.
	if Admit(1, getAccount(t, userID).Total) {
		t.Fatal("frozen overdraft account must not admit a new request")
	}
	_, err = s.Prehold(t.Context(), PreholdParams{
		RequestID: newRequestID("g02freeze"), UserID: userID, APIKeyID: 1, ModelID: 1, NodeID: "test",
		RawEstMicro: 1, EstMicro: 1, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	})
	if !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("new prehold while frozen err = %v, want ErrInsufficientCredits", err)
	}

	recharge, err := s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 2000000, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("Recharge: %v", err)
	}
	if recharge.OffsetMicro != 1100000 {
		t.Fatalf("offset = %d, want 1100000", recharge.OffsetMicro)
	}
	if st := getAccount(t, userID); st.Total != 400000 {
		t.Fatalf("balance after recharge = %d, want 400000", st.Total)
	}
	if arrears, ok := getArrears(t, requestID); !ok || arrears.State != ArrearsStateRepaid || arrears.Settled != 1100000 {
		t.Fatalf("arrears after recharge = %+v (exists=%v), want REPAID/1100000", arrears, ok)
	}
	if status := getUserStatus(t, userID); status != UserStatusNormal {
		t.Fatalf("status after recharge = %s, want NORMAL", status)
	}
	t.Logf("G-02 hard truncation: cost=2000000 deducted=%d arrears=%d balance_before_recharge=%d; recharge=2000000 offset=%d balance_after=%d status=%s",
		res.ConsumedMicro, res.ArrearsMicro, -500000, recharge.OffsetMicro,
		getAccount(t, userID).Total, getUserStatus(t, userID))
}

// G-01: 1,200 input + 800 output at 1,500/4,500 = 5,400; the balance goes
// 1,000,000 -> 994,600 and a second settle is idempotent.
func TestG01SettleOnceAndIdempotentRetry(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 1000000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 1000000, Remaining: 1000000})

	requestID := newRequestID("g01")
	preholdFor(t, s, userID, 300000, 300000, requestID)

	first, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 5400,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if first.ConsumedMicro != 5400 {
		t.Fatalf("consumed = %d, want 5400", first.ConsumedMicro)
	}
	if st := getAccount(t, userID); st.Total != 994600 {
		t.Fatalf("balance = %d, want 994600", st.Total)
	}

	second, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 5400,
	})
	if err != nil {
		t.Fatalf("second Settle: %v", err)
	}
	if !second.Idempotent {
		t.Fatal("second settle must be idempotent")
	}
	if second.ConsumedMicro != 5400 {
		t.Fatalf("idempotent consumed = %d, want 5400", second.ConsumedMicro)
	}
	if st := getAccount(t, userID); st.Total != 994600 {
		t.Fatalf("balance after replay = %d, want 994600", st.Total)
	}
	if got := sumEntries(t, userID, BizTypeConsumeSettle); got != -5400 {
		t.Fatalf("settle entry sum = %d, want -5400", got)
	}
}

// A8: consumed = 0 releases the full prehold and writes only PREHOLD_RELEASE.
func TestA8FailedRequestReleasesFullPrehold(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 10000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 10000, Remaining: 10000})

	requestID := newRequestID("a8fail")
	pre := preholdFor(t, s, userID, 6000, 7200, requestID)
	if st := getAccount(t, userID); st.Total != 10000-pre.HoldMicro {
		t.Fatalf("balance after prehold = %d, want %d", st.Total, 10000-pre.HoldMicro)
	}

	res, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 0, ReleaseReason: "UPSTREAM_FAIL",
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if res.State != HoldStateReleased || res.ReleasedMicro != pre.HoldMicro {
		t.Fatalf("release result = %+v, want RELEASED / %d", res, pre.HoldMicro)
	}
	if st := getAccount(t, userID); st.Total != 10000 {
		t.Fatalf("balance after release = %d, want 10000", st.Total)
	}
	if entries := ledgerEntries(t, userID, BizTypeConsumeSettle); len(entries) != 0 {
		t.Fatalf("failed request wrote %d CONSUME_SETTLE entries, want 0", len(entries))
	}
	releases := ledgerEntries(t, userID, BizTypePreholdRelease)
	if len(releases) != 1 || releases[0].AmountTotal != pre.HoldMicro {
		t.Fatalf("release entries = %s, want one +%d", describeEntries(releases), pre.HoldMicro)
	}
	if state, _ := getHoldState(t, requestID); state != HoldStateReleased {
		t.Fatalf("hold state = %s, want RELEASED", state)
	}
}

// A8 second half: a hold row without the matching prehold ledger entry must
// be refused. Releasing it would create credits from nothing.
func TestA8ReleaseWithoutPreholdLedgerIsRejected(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 10000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 10000, Remaining: 10000})

	requestID := newRequestID("a8orphan")
	insertHoldDirect(t, requestID, userID, 3250, HoldStateHeld)

	_, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 0, ReleaseReason: "UPSTREAM_FAIL",
	})
	if !errors.Is(err, ErrPreholdLedgerMissing) {
		t.Fatalf("err = %v, want ErrPreholdLedgerMissing", err)
	}
	if st := getAccount(t, userID); st.Total != 10000 || st.Version != 0 {
		t.Fatalf("balance changed despite rejected release: %+v", st)
	}
	if state, _ := getHoldState(t, requestID); state != HoldStateHeld {
		t.Fatalf("hold state = %s, want HELD", state)
	}
}

// G-02 negative-balance example: balance 3,000, hold 1,500, actual 3,500
// => -500; recharge 5,000 => 4,500.
func TestG02SmallNegativeBalanceAndRecharge(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 3000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 3000, Remaining: 3000})

	requestID := newRequestID("neg")
	pre := preholdFor(t, s, userID, 3000, 3500, requestID)
	if pre.HoldMicro != 1500 {
		t.Fatalf("hold = %d, want 1500", pre.HoldMicro)
	}
	if _, err := s.Settle(t.Context(), SettleParams{
		RequestID: requestID, UserID: userID, ConsumedMicro: 3500,
	}); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if st := getAccount(t, userID); st.Total != -500 {
		t.Fatalf("balance = %d, want -500", st.Total)
	}

	res, err := s.Recharge(t.Context(), RechargeParams{
		UserID: userID, AmountMicro: 5000, RechargeOrderID: uniqueOrderID(),
	})
	if err != nil {
		t.Fatalf("Recharge: %v", err)
	}
	if res.OffsetMicro != 0 {
		t.Fatalf("offset = %d, want 0 (no arrears)", res.OffsetMicro)
	}
	if st := getAccount(t, userID); st.Total != 4500 {
		t.Fatalf("balance after recharge = %d, want 4500", st.Total)
	}
}

func TestSettleUnknownRequest(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Settle(t.Context(), SettleParams{
		RequestID: newRequestID("missing"), UserID: 1, ConsumedMicro: 0,
	})
	if !errors.Is(err, ErrPreholdNotFound) {
		t.Fatalf("err = %v, want ErrPreholdNotFound", err)
	}
}
