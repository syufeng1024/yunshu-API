package fund

import (
	"context"
	"net/http"
	"testing"
)

// A3 supplement: a non-PAID order never credits, whatever the notice says.
// The existing TestPendingOrderCannotBeCredited only proves that a forged
// amount is rejected; it never exercises the state guard itself. This test
// covers both branches that keep t_credit_account untouched:
//
//   - PENDING + a channel FAILED notice (the order is still unpaid);
//   - CLOSED + a perfectly signed SUCCESS notice (late payment).
func TestA3NonPaidOrderNeverCredits(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")

	// 1. PENDING order, channel reports failure. Nothing may be credited.
	pending, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})
	failedBody := map[string]any{
		"out_trade_no": pending.OrderNo,
		"trade_no":     "WXFAILED001",
		"amount_fen":   pending.PayAmountFen,
		"status":       callbackStatusFailed,
		"sign": signCallback(testPaySecret, pending.OrderNo, "WXFAILED001",
			pending.PayAmountFen, callbackStatusFailed),
	}
	rec := callCallback(t, svc, "wechat", failedBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PENDING+FAILED status=%d body=%s", rec.Code, rec.Body.String())
	}
	if st := orderState(t, pending.ID); st != OrderStatePending {
		t.Fatalf("PENDING+FAILED order state=%q, want PENDING", st)
	}
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("PENDING+FAILED balance=%d, want 0", b.Total)
	}
	if n := countLedger(t, uid); n != 0 {
		t.Fatalf("PENDING+FAILED ledger entries=%d, want 0", n)
	}
	if n := countBatches(t, uid); n != 0 {
		t.Fatalf("PENDING+FAILED batches=%d, want 0", n)
	}

	// 2. CLOSED order, a valid signed SUCCESS notice. The state guard must
	// refuse the credit and answer 200 so the channel stops retrying.
	closed, _ := createOrder(t, svc, uid, map[string]any{
		"order_type": OrderTypePlan, "plan_id": plan.ID, "pay_channel": PayChannelWechat,
	})
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_recharge_order SET state = 'CLOSED' WHERE id = ?`, closed.ID); err != nil {
		t.Fatal(err)
	}
	rec = successfulCallback(t, svc, closed, "WXCLOSED001")
	if rec.Code != http.StatusOK {
		t.Fatalf("CLOSED+SUCCESS status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp callbackResponse
	decodeBody(t, rec, &resp)
	if resp.Credited || resp.Replayed {
		t.Fatalf("CLOSED+SUCCESS response=%+v, want credited=false replayed=false", resp)
	}
	if resp.Reason != anomalyOrderClosed {
		t.Fatalf("CLOSED+SUCCESS reason=%q, want %q", resp.Reason, anomalyOrderClosed)
	}
	if st := orderState(t, closed.ID); st != OrderStateClosed {
		t.Fatalf("CLOSED+SUCCESS order state=%q, want CLOSED", st)
	}
	b := balanceOf(t, uid)
	if b.Recharge != 0 || b.Bonus != 0 || b.Total != 0 {
		t.Fatalf("CLOSED+SUCCESS balance=%+v, want all zero", b)
	}
	if n := countLedger(t, uid); n != 0 {
		t.Fatalf("CLOSED+SUCCESS ledger entries=%d, want 0", n)
	}
	if n := countBatches(t, uid); n != 0 {
		t.Fatalf("CLOSED+SUCCESS batches=%d, want 0", n)
	}
	t.Logf("A3: PENDING+FAILED and CLOSED+SUCCESS both credited nothing")
}

// A4 first path: with both bonus (2,000) and recharge (9,900) available,
// consuming 3,000 burns the whole bonus batch first (2,000) and 1,000 recharge.
// The refundable base is therefore 8,900 credits = 89,000,000 micro, which is
// 8,010 fen cash at the fixed 90% rate (fee 890 fen). Bonus never participates.
//
// The existing TestA4RefundExactG04Numbers covers only the PRD 7,900 path
// (bonus partially expired before the second debit); this test covers the
// default path where all bonus is still usable.
func TestA4RefundableDefaultPath8900(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	plan := planByCode(t, "STANDARD")

	// Default timeline: bonus is fully valid, so FIFO burns 2,000 bonus then
	// 1,000 recharge.
	consumeDebits(t, svc, uid, 3000)

	b := balanceOf(t, uid)
	if b.Recharge != plan.GrantRechargeMicro-1000*MicroPerCredit {
		t.Fatalf("recharge after 3,000 consume = %d micro, want %d (8,900 credits)",
			b.Recharge, plan.GrantRechargeMicro-1000*MicroPerCredit)
	}
	if b.Bonus != 0 {
		t.Fatalf("bonus after 3,000 consume = %d micro, want 0 (2,000 bonus burnt first)", b.Bonus)
	}
	if b.Total != 89_000_000 {
		t.Fatalf("total after 3,000 consume = %d micro, want 89,000,000", b.Total)
	}

	rec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("refund apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp createRefundResponse
	decodeBody(t, rec, &resp)
	v := resp.Refund

	// Exact numbers for the 8,900 path. Each is asserted separately so a
	// failure names the value that moved.
	if v.ApplyMicro != 89_000_000 || v.RefundMicro != 89_000_000 {
		t.Fatalf("apply/refund micro = %d/%d, want 89,000,000", v.ApplyMicro, v.RefundMicro)
	}
	if v.ApplyCredits != 8900 || v.RefundCredits != 8900 {
		t.Fatalf("apply/refund credits = %d/%d, want 8900", v.ApplyCredits, v.RefundCredits)
	}
	if v.RefundFen != 8010 {
		t.Fatalf("refund_fen = %d, want 8010 (80.10 元)", v.RefundFen)
	}
	if v.FeeFen != 890 {
		t.Fatalf("fee_fen = %d, want 890", v.FeeFen)
	}
	if v.State != RefundStateApplied {
		t.Fatalf("refund state = %q, want APPLIED", v.State)
	}
	if got := frozenOf(t, uid); got != 89_000_000 {
		t.Fatalf("frozen_refund_micro = %d, want 89,000,000", got)
	}
	if st := orderState(t, order.ID); st != OrderStateRefunding {
		t.Fatalf("order state = %q, want REFUNDING", st)
	}
	if b := balanceOf(t, uid); b.Total != 89_000_000 {
		t.Fatalf("balance during freeze = %d, want 89,000,000 (freeze moves nothing)", b.Total)
	}

	// The bonus batch is excluded by construction, not by arithmetic.
	var bonusRefundable, bonusRemaining int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT refundable, remaining_micro FROM t_credit_batch
		 WHERE user_id = ? AND batch_type = 'BONUS'`, uid).
		Scan(&bonusRefundable, &bonusRemaining); err != nil {
		t.Fatal(err)
	}
	if bonusRefundable != 0 || bonusRemaining != 0 {
		t.Fatalf("bonus batch refundable/remaining = %d/%d, want 0/0", bonusRefundable, bonusRemaining)
	}

	t.Logf("default path: consume=3000 recharge=%d bonus=%d apply=%d refund_fen=%d fee_fen=%d frozen=%d",
		b.Recharge, b.Bonus, v.ApplyMicro, v.RefundFen, v.FeeFen, frozenOf(t, uid))
}
