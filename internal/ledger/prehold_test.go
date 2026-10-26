package ledger

import (
	"errors"
	"testing"
	"time"
)

// G-02 discriminator: admission compares RAW_EST with available, not est.
func TestG02AdmitUsesRawEstimate(t *testing.T) {
	if !Admit(6000, 6500) {
		t.Fatal("raw_est 6000 <= available 6500 must be admitted")
	}
	if Admit(7200, 6500) {
		t.Fatal("est 7200 > available 6500 must not be the admission criterion")
	}
	if Admit(6001, 6000) {
		t.Fatal("raw_est above available must be rejected")
	}
	if !Admit(6000, 6000) {
		t.Fatal("raw_est equal to available must be admitted")
	}
}

// The prehold is MIN(est, available × 50%), floored. With the G-02 boundary
// values the correct hold is 3,250 (6500 × 50%). The task card's assertion
// says 3,600 and labels it "6500×50%"; 3,600 is actually 7200×50% and would
// violate the 50% cap. The formula is authoritative.
func TestG02HoldIsFiftyPercentOfAvailable(t *testing.T) {
	got := HoldMicro(7200, 6500, 5000)
	if got != 3250 {
		t.Fatalf("HoldMicro(7200,6500,5000)=%d, want 3250 (6500×50%%, floored)", got)
	}
	t.Logf("G-02 hold evidence: available=6500 est=7200 => 6500×50%%=3250, est×50%%=3600; formula MIN(est, available×50%%)=3250")
	if got > 6500*5000/10000 {
		t.Fatalf("hold %d exceeds the 50%% cap %d", got, 6500*5000/10000)
	}
}

func TestHoldNeverExceedsAvailableCap(t *testing.T) {
	cases := []struct {
		est, available, ratio, want int64
	}{
		{7200, 7200, 5000, 3600},  // the only way to get the card's 3,600
		{5000, 10000, 5000, 5000}, // est is the smaller value
		{7200, 10000, 5000, 5000}, // cap is the smaller value
		{7200, 0, 5000, 0},
		{7200, -100, 5000, 0}, // overdraft is not spendable at prehold
		{7200, 6500, 0, 0},
	}
	for _, c := range cases {
		if got := HoldMicro(c.est, c.available, c.ratio); got != c.want {
			t.Fatalf("HoldMicro(%d,%d,%d)=%d, want %d", c.est, c.available, c.ratio, got, c.want)
		}
	}
}

// G-02 integration: balance 6,500, raw_est 6,000, est 7,200 => admit, hold
// 3,250, settle 6,000, final balance 500.
func TestG02PreholdAndSettleBoundary(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 6500, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 6500, Remaining: 6500})

	requestID := newRequestID("g02")
	pre, err := s.Prehold(t.Context(), PreholdParams{
		RequestID:      requestID,
		UserID:         userID,
		APIKeyID:       1,
		ModelID:        1,
		NodeID:         "test-node",
		RawEstMicro:    6000,
		EstMicro:       7200,
		MaxHoldRatioBP: 5000,
		ExpiresAt:      s.Clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Prehold: %v", err)
	}
	if pre.HoldMicro != 3250 {
		t.Fatalf("hold = %d, want 3250", pre.HoldMicro)
	}
	if pre.AvailableBeforeMicro != 6500 {
		t.Fatalf("available before = %d, want 6500", pre.AvailableBeforeMicro)
	}
	if st := getAccount(t, userID); st.Total != 3250 {
		t.Fatalf("balance after prehold = %d, want 3250", st.Total)
	}
	t.Logf("G-02 prehold: raw_est=6000 available_before=6500 admitted=%v hold=%d balance_after_prehold=%d",
		Admit(6000, 6500), pre.HoldMicro, getAccount(t, userID).Total)

	settle, err := s.Settle(t.Context(), SettleParams{
		RequestID:     requestID,
		UserID:        userID,
		ConsumedMicro: 6000,
	})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settle.ConsumedMicro != 6000 || settle.ArrearsMicro != 0 {
		t.Fatalf("settle = %+v, want consumed 6000 / arrears 0", settle)
	}
	if st := getAccount(t, userID); st.Total != 500 {
		t.Fatalf("balance after settle = %d, want 500", st.Total)
	}
	if got := sumEntries(t, userID, BizTypeConsumeSettle); got != -6000 {
		t.Fatalf("settle entry sum = %d, want -6000", got)
	}
	t.Logf("G-02 settle: consumed=%d batch_backed=%d overdraft=%d arrears=%d balance_after=%d entry_sum=%d",
		settle.ConsumedMicro, settle.BatchBackedMicro, settle.OverdraftMicro,
		settle.ArrearsMicro, getAccount(t, userID).Total, sumEntries(t, userID, BizTypeConsumeSettle))
}

// G-02 "截断后放行": est > available×50% does not reject the request; the hold
// is capped at 5,000 and settlement proceeds.
func TestG02HoldTruncatedButRequestAdmitted(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 10000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 10000, Remaining: 10000})

	requestID := newRequestID("g02c")
	pre, err := s.Prehold(t.Context(), PreholdParams{
		RequestID: requestID, UserID: userID, APIKeyID: 1, ModelID: 1, NodeID: "test",
		RawEstMicro: 6000, EstMicro: 7200, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Prehold: %v", err)
	}
	if pre.HoldMicro != 5000 {
		t.Fatalf("hold = %d, want 5000 (capped at 10000×50%%)", pre.HoldMicro)
	}
}

func TestG02InsufficientCreditsDoesNotMoveMoney(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 10000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 10000, Remaining: 10000})

	requestID := newRequestID("g02r")
	_, err := s.Prehold(t.Context(), PreholdParams{
		RequestID: requestID, UserID: userID, APIKeyID: 1, ModelID: 1, NodeID: "test",
		RawEstMicro: 52500, EstMicro: 63000, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	})
	if !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("err = %v, want ErrInsufficientCredits", err)
	}
	if st := getAccount(t, userID); st.Total != 10000 || st.Version != 0 {
		t.Fatalf("account moved on rejection: %+v", st)
	}
	if entries := ledgerEntries(t, userID, ""); len(entries) != 0 {
		t.Fatalf("ledger entries on rejection = %d, want 0", len(entries))
	}
}

func TestPreholdIsIdempotentByRequestID(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 10000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 10000, Remaining: 10000})

	requestID := newRequestID("idem")
	p := PreholdParams{
		RequestID: requestID, UserID: userID, APIKeyID: 1, ModelID: 1, NodeID: "test",
		RawEstMicro: 6000, EstMicro: 7200, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	}
	first, err := s.Prehold(t.Context(), p)
	if err != nil {
		t.Fatalf("first Prehold: %v", err)
	}
	second, err := s.Prehold(t.Context(), p)
	if err != nil {
		t.Fatalf("second Prehold: %v", err)
	}
	if !second.Idempotent {
		t.Fatal("second prehold must be idempotent")
	}
	if second.HoldMicro != first.HoldMicro {
		t.Fatalf("hold changed on replay: %d -> %d", first.HoldMicro, second.HoldMicro)
	}
	entries := ledgerEntries(t, userID, BizTypeConsumePrehold)
	if len(entries) != 1 {
		t.Fatalf("prehold entries = %d, want 1", len(entries))
	}
	if st := getAccount(t, userID); st.Total != 10000-first.HoldMicro {
		t.Fatalf("balance after replay = %d, want %d", st.Total, 10000-first.HoldMicro)
	}
}
