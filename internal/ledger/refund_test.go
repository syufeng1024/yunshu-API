package ledger

import (
	"errors"
	"testing"
	"time"
)

// G-04 setup: 9,900 recharge + 2,000 bonus; 3,000 consumed as 1,000 bonus /
// 2,000 recharge. Unspent recharge = 7,900 credits => 71.10 yuan (7,110 fen).
func TestG04RefundBaseExcludesBonusAndConsumed(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 10000000, 79000000, 500000)
	orderID := newRechargeOrder(t, userID, 99000000)

	// Recharge batch: face 9,900 credits, 2,000 consumed, 7,900 remaining.
	rechargeBatch := newBatch(t, userID, batchOpts{
		Refundable: true, Face: 99000000, Remaining: 79000000,
		CreatedAt: fixtureBase.Add(-2 * time.Hour), SourceID: &orderID,
	})
	// Bonus batch: face 2,000, 1,000 consumed, 1,000 remaining. Not refundable.
	newBatch(t, userID, batchOpts{
		Refundable: false, Face: 20000000, Remaining: 10000000,
		CreatedAt: fixtureBase.Add(-time.Hour),
	})

	quote, err := s.QuoteRefund(t.Context(), RefundQuoteParams{
		UserID: userID, RechargeOrderID: &orderID,
	})
	if err != nil {
		t.Fatalf("QuoteRefund: %v", err)
	}
	if quote.RefundableMicro != 79000000 {
		t.Fatalf("refundable = %d, want 79000000 (7,900 credits)", quote.RefundableMicro)
	}
	if quote.RefundFen != 7110 {
		t.Fatalf("refund fen = %d, want 7110 (71.10 yuan)", quote.RefundFen)
	}
	if quote.FeeFen != 790 {
		t.Fatalf("fee fen = %d, want 790", quote.FeeFen)
	}
	if !quote.Eligible {
		t.Fatalf("quote not eligible: %s", quote.Reason)
	}

	refundOrderID := newRefundOrder(t, userID, orderID)
	res, err := s.Refund(t.Context(), RefundParams{
		UserID: userID, RechargeOrderID: orderID, RefundOrderID: refundOrderID,
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if res.RefundableMicro != 79000000 || res.RefundFen != 7110 || res.FeeFen != 790 {
		t.Fatalf("refund result = %+v, want 79000000/7110/790", res)
	}
	if res.BalanceAfterMicro != 10000000 {
		t.Fatalf("balance after refund = %d, want 10000000 (the bonus remains)", res.BalanceAfterMicro)
	}
	// The bonus batch is untouched.
	if res.BonusRemainingMicro != 10000000 {
		t.Fatalf("bonus remaining = %d, want 10000000", res.BonusRemainingMicro)
	}
	var rechargeRemaining int64
	if err := testDB.QueryRow(
		`SELECT remaining_micro FROM t_credit_batch WHERE id = ?`, rechargeBatch,
	).Scan(&rechargeRemaining); err != nil {
		t.Fatalf("read recharge batch: %v", err)
	}
	if rechargeRemaining != 0 {
		t.Fatalf("recharge batch remaining = %d, want 0 after refunding all 7,900", rechargeRemaining)
	}

	var state string
	var refundFen, feeFen int
	var applyMicro int64
	if err := testDB.QueryRow(
		`SELECT state, refund_fen, fee_fen, apply_micro FROM t_refund_order WHERE id = ?`,
		refundOrderID,
	).Scan(&state, &refundFen, &feeFen, &applyMicro); err != nil {
		t.Fatalf("read refund order: %v", err)
	}
	if state != "PAID" || refundFen != 7110 || feeFen != 790 || applyMicro != 79000000 {
		t.Fatalf("refund order = state %s fen %d fee %d apply %d, want PAID/7110/790/79000000",
			state, refundFen, feeFen, applyMicro)
	}
	t.Logf("G-04: refundable=%d micro (7900 credits) refund=%d fen (71.10 yuan) fee=%d fen balance_after=%d bonus_remaining=%d",
		res.RefundableMicro, res.RefundFen, res.FeeFen, res.BalanceAfterMicro, res.BonusRemainingMicro)

	// 90% formula unit checks.
	if got := RefundFen(10000000); got != 900 {
		t.Fatalf("RefundFen(10,000,000) = %d, want 900 (10 yuan × 90%%)", got)
	}
	if got := RefundFen(0); got != 0 {
		t.Fatalf("RefundFen(0) = %d, want 0", got)
	}
}

func TestG04MinimumRefundThreshold(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 9000000, 500000)
	orderID := newRechargeOrder(t, userID, 9000000)
	newBatch(t, userID, batchOpts{
		Refundable: true, Face: 9000000, Remaining: 9000000,
		CreatedAt: fixtureBase, SourceID: &orderID,
	})

	quote, err := s.QuoteRefund(t.Context(), RefundQuoteParams{
		UserID: userID, RechargeOrderID: &orderID,
	})
	if err != nil {
		t.Fatalf("QuoteRefund: %v", err)
	}
	if quote.Eligible {
		t.Fatal("900 credits is below the 1,000-credit minimum and must be rejected")
	}
	if quote.Reason == "" {
		t.Fatal("rejection must state the minimum threshold")
	}
}

func TestG04IssuedInvoiceBlocksRefund(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 79000000, 500000)
	orderID := newRechargeOrder(t, userID, 79000000)
	newBatch(t, userID, batchOpts{
		Refundable: true, Face: 79000000, Remaining: 79000000,
		CreatedAt: fixtureBase, SourceID: &orderID,
	})
	newInvoice(t, userID, orderID, "ISSUED")
	refundOrderID := newRefundOrder(t, userID, orderID)

	quote, err := s.QuoteRefund(t.Context(), RefundQuoteParams{
		UserID: userID, RechargeOrderID: &orderID,
	})
	if err != nil {
		t.Fatalf("QuoteRefund: %v", err)
	}
	if quote.Eligible || quote.Reason == "" {
		t.Fatalf("issued invoice quote = %+v, want rejection with reason", quote)
	}

	_, err = s.Refund(t.Context(), RefundParams{
		UserID: userID, RechargeOrderID: orderID, RefundOrderID: refundOrderID,
	})
	if !errors.Is(err, ErrRefundIneligible) {
		t.Fatalf("refund err = %v, want ErrRefundIneligible", err)
	}
	if st := getAccount(t, userID); st.Total != 79000000 {
		t.Fatalf("balance changed on rejected refund: %d", st.Total)
	}
	if entries := ledgerEntries(t, userID, BizTypeRefund); len(entries) != 0 {
		t.Fatalf("refund wrote %d entries on rejection, want 0", len(entries))
	}
}

func TestG04PendingInvoiceRequiresRevoke(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 79000000, 500000)
	orderID := newRechargeOrder(t, userID, 79000000)
	newBatch(t, userID, batchOpts{
		Refundable: true, Face: 79000000, Remaining: 79000000,
		CreatedAt: fixtureBase, SourceID: &orderID,
	})
	newInvoice(t, userID, orderID, "PENDING")

	quote, err := s.QuoteRefund(t.Context(), RefundQuoteParams{
		UserID: userID, RechargeOrderID: &orderID,
	})
	if err != nil {
		t.Fatalf("QuoteRefund: %v", err)
	}
	if quote.Eligible {
		t.Fatal("pending invoice must block until revoked")
	}
	if quote.InvoiceState != "PENDING" {
		t.Fatalf("invoice state = %s, want PENDING", quote.InvoiceState)
	}
}

func TestBonusOnlyIsNotRefundable(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 20000000, 0, 500000)
	orderID := newRechargeOrder(t, userID, 0)
	newBatch(t, userID, batchOpts{
		Refundable: false, Face: 20000000, Remaining: 20000000,
		CreatedAt: fixtureBase,
	})

	quote, err := s.QuoteRefund(t.Context(), RefundQuoteParams{
		UserID: userID, RechargeOrderID: &orderID,
	})
	if err != nil {
		t.Fatalf("QuoteRefund: %v", err)
	}
	if quote.RefundableMicro != 0 || quote.Eligible {
		t.Fatalf("bonus-only quote = %+v, want 0 / not eligible", quote)
	}
}

func TestRefundOrderStateGuards(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 79000000, 500000)
	orderID := newRechargeOrder(t, userID, 79000000)
	newBatch(t, userID, batchOpts{
		Refundable: true, Face: 79000000, Remaining: 79000000,
		CreatedAt: fixtureBase, SourceID: &orderID,
	})

	// A missing refund order is a validation error, never an anonymous refund.
	_, err := s.Refund(t.Context(), RefundParams{
		UserID: userID, RechargeOrderID: orderID, RefundOrderID: 999999999,
	})
	if !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("missing refund order err = %v, want ErrInvalidParam", err)
	}

	// PAID is idempotent even if the ledger LIKE fast path misses.
	paidID := newRefundOrder(t, userID, orderID)
	execOrFail(t, `UPDATE t_refund_order SET state = 'PAID' WHERE id = ?`, paidID)
	res, err := s.Refund(t.Context(), RefundParams{
		UserID: userID, RechargeOrderID: orderID, RefundOrderID: paidID,
	})
	if err != nil {
		t.Fatalf("PAID refund: %v", err)
	}
	if !res.Idempotent || res.RefundableMicro != 0 {
		t.Fatalf("PAID refund = %+v, want idempotent/0", res)
	}

	// REJECTED / FAILED orders are not refundable.
	rejectedID := newRefundOrder(t, userID, orderID)
	execOrFail(t, `UPDATE t_refund_order SET state = 'REJECTED' WHERE id = ?`, rejectedID)
	if _, err := s.Refund(t.Context(), RefundParams{
		UserID: userID, RechargeOrderID: orderID, RefundOrderID: rejectedID,
	}); !errors.Is(err, ErrRefundIneligible) {
		t.Fatalf("REJECTED refund err = %v, want ErrRefundIneligible", err)
	}
}

func TestRefundIsIdempotentByOrder(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 79000000, 500000)
	orderID := newRechargeOrder(t, userID, 79000000)
	newBatch(t, userID, batchOpts{
		Refundable: true, Face: 79000000, Remaining: 79000000,
		CreatedAt: fixtureBase, SourceID: &orderID,
	})
	refundOrderID := newRefundOrder(t, userID, orderID)

	p := RefundParams{UserID: userID, RechargeOrderID: orderID, RefundOrderID: refundOrderID}
	first, err := s.Refund(t.Context(), p)
	if err != nil {
		t.Fatalf("first Refund: %v", err)
	}
	second, err := s.Refund(t.Context(), p)
	if err != nil {
		t.Fatalf("second Refund: %v", err)
	}
	if !second.Idempotent {
		t.Fatal("second refund must be idempotent")
	}
	if second.RefundableMicro != first.RefundableMicro {
		t.Fatalf("refund amount changed on replay: %d -> %d", first.RefundableMicro, second.RefundableMicro)
	}
	if st := getAccount(t, userID); st.Total != 0 {
		t.Fatalf("balance after replay = %d, want 0", st.Total)
	}
	if entries := ledgerEntries(t, userID, BizTypeRefund); len(entries) != 1 {
		t.Fatalf("refund entries = %d, want 1", len(entries))
	}
}
