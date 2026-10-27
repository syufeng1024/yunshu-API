package fund

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A4 / G-04: 99 元 → 9,900 recharge + 2,000 bonus; consume 3,000 (bonus 1,000 /
// recharge 2,000); refundable = 7,900 credits; cash = 7,110 fen (71.10 元);
// fee = 790 fen.
func TestA4RefundExactG04Numbers(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	plan := planByCode(t, "STANDARD")

	consumeG04(t, svc, uid)
	b := balanceOf(t, uid)
	if b.Recharge != plan.GrantRechargeMicro-2000*MicroPerCredit {
		t.Fatalf("recharge after consume = %d, want 7,900 credits (%d micro)",
			b.Recharge/MicroPerCredit, plan.GrantRechargeMicro-2000*MicroPerCredit)
	}
	if b.Bonus != plan.GrantBonusMicro-1000*MicroPerCredit {
		t.Fatalf("bonus after consume = %d, want 1,000 credits (%d micro)",
			b.Bonus/MicroPerCredit, plan.GrantBonusMicro-1000*MicroPerCredit)
	}
	t.Logf("after consume: recharge=%d bonus=%d total=%d micro",
		b.Recharge, b.Bonus, b.Total)

	rec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("refund apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp createRefundResponse
	decodeBody(t, rec, &resp)
	v := resp.Refund
	if v.ApplyMicro != 79_000_000 || v.RefundMicro != 79_000_000 {
		t.Fatalf("apply/refund micro = %d/%d, want 79,000,000", v.ApplyMicro, v.RefundMicro)
	}
	if v.RefundFen != 7110 {
		t.Fatalf("refund_fen = %d, want 7110 (71.10 元)", v.RefundFen)
	}
	if v.FeeFen != 790 {
		t.Fatalf("fee_fen = %d, want 790", v.FeeFen)
	}
	if v.State != RefundStateApplied {
		t.Fatalf("state = %q, want APPLIED", v.State)
	}
	if got := frozenOf(t, uid); got != 79_000_000 {
		t.Fatalf("frozen_refund_micro = %d, want 79,000,000", got)
	}
	if st := orderState(t, order.ID); st != OrderStateRefunding {
		t.Fatalf("recharge order state = %q, want REFUNDING", st)
	}
	if b := balanceOf(t, uid); b.Total != 89_000_000 {
		t.Fatalf("balance total during refund = %d, want 89,000,000 (freeze moves nothing)", b.Total)
	}
	t.Logf("quote: apply=%d micro refund_fen=%d fee_fen=%d frozen=%d",
		v.ApplyMicro, v.RefundFen, v.FeeFen, frozenOf(t, uid))

	// T16 path: approve (another operator) → paying → paid.
	if err := svc.ApproveRefund(context.Background(), v.ID, 900001); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := svc.StartRefundPayout(context.Background(), v.ID); err != nil {
		t.Fatalf("paying: %v", err)
	}
	result, err := svc.MarkRefundPaid(context.Background(), v.ID, "WXREFUND0001")
	if err != nil {
		t.Fatalf("paid: %v", err)
	}
	if result.RefundFen != 7110 || result.FeeFen != 790 {
		t.Fatalf("ledger result refund/fee = %d/%d, want 7110/790", result.RefundFen, result.FeeFen)
	}
	if st := refundStateOf(t, v.ID); st != RefundStatePaid {
		t.Fatalf("refund state = %q, want PAID", st)
	}
	b = balanceOf(t, uid)
	if b.Recharge != 0 || b.Bonus != 10_000_000 || b.Total != 10_000_000 {
		t.Fatalf("balance after refund = recharge %d bonus %d total %d, want 0/10,000,000/10,000,000",
			b.Recharge, b.Bonus, b.Total)
	}
	if got := frozenOf(t, uid); got != 0 {
		t.Fatalf("frozen after PAID = %d, want 0", got)
	}
	if st := orderState(t, order.ID); st != OrderStateRefunded {
		t.Fatalf("recharge order state = %q, want REFUNDED", st)
	}
	if got := orderRefundableMicro(t, order.ID); got != 0 {
		t.Fatalf("order refundable_micro = %d, want 0", got)
	}
	var refundEntries int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_ledger_entry WHERE refund_order_id = ? AND biz_type = 'REFUND'`,
		v.ID).Scan(&refundEntries); err != nil {
		t.Fatal(err)
	}
	if refundEntries != 1 {
		t.Fatalf("REFUND entries = %d, want 1 (one batch was still populated)", refundEntries)
	}
	if n := countNotifications(t, uid, eventRefundArrived); n != 1 {
		t.Fatalf("REFUND_ARRIVED notifications = %d, want 1", n)
	}
	t.Logf("paid: refund_fen=%d fee_fen=%d balance=%d/%d frozen=%d entries=%d",
		result.RefundFen, result.FeeFen, b.Recharge, b.Bonus, frozenOf(t, uid), refundEntries)
}

// A10: a failed payout releases the freeze and leaves the totals untouched.
func TestA10FailedRefundReleasesFreezeBalanceUnchanged(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	consumeG04(t, svc, uid)

	rec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("refund apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp createRefundResponse
	decodeBody(t, rec, &resp)
	before := balanceOf(t, uid)
	if before.Total != 89_000_000 {
		t.Fatalf("balance before failure = %d, want 89,000,000", before.Total)
	}
	if err := svc.ApproveRefund(context.Background(), resp.Refund.ID, 900002); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartRefundPayout(context.Background(), resp.Refund.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRefundFailed(context.Background(), resp.Refund.ID, "渠道返回超期，原路退回失败"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	after := balanceOf(t, uid)
	if after != before {
		t.Fatalf("balance changed on failed refund: before=%+v after=%+v", before, after)
	}
	if got := frozenOf(t, uid); got != 0 {
		t.Fatalf("frozen after FAILED = %d, want 0", got)
	}
	if st := refundStateOf(t, resp.Refund.ID); st != RefundStateFailed {
		t.Fatalf("refund state = %q, want FAILED", st)
	}
	if st := orderState(t, order.ID); st != OrderStatePaid {
		t.Fatalf("recharge order state = %q, want PAID", st)
	}
	if got := orderRefundableMicro(t, order.ID); got != 79_000_000 {
		t.Fatalf("order refundable_micro = %d, want 79,000,000 restored", got)
	}
	var entries int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_ledger_entry WHERE refund_order_id = ?`,
		resp.Refund.ID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 0 {
		t.Fatalf("failed refund wrote %d ledger entries, want 0", entries)
	}
	if n := countNotifications(t, uid, eventRefundArrived); n != 1 {
		t.Fatalf("REFUND_ARRIVED notifications = %d, want 1", n)
	}
	t.Logf("after FAILED: balance=%d/%d total=%d frozen=%d state=%s order=%s refundable=%d",
		after.Recharge, after.Bonus, after.Total, frozenOf(t, uid),
		refundStateOf(t, resp.Refund.ID), orderState(t, order.ID),
		orderRefundableMicro(t, order.ID))
}

// The same Idempotency-Key replays the first refund row instead of creating a
// second one with a second freeze.
func TestRefundIdempotencyKeyReplays(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	consumeG04(t, svc, uid)

	headers := map[string]string{"Idempotency-Key": "refund-replay-1"}
	first := callWithHeaders(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := callWithHeaders(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid, headers)
	if second.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	var a, b createRefundResponse
	decodeBody(t, first, &a)
	decodeBody(t, second, &b)
	if !b.Replayed || a.Refund.ID != b.Refund.ID {
		t.Fatalf("replay = %+v (first id %d)", b, a.Refund.ID)
	}
	var n int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_refund_order WHERE user_id = ?`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("refund rows = %d, want 1", n)
	}
	if got := frozenOf(t, uid); got != 79_000_000 {
		t.Fatalf("frozen = %d, want 79,000,000 (not doubled)", got)
	}
}

// An ISSUED invoice blocks a refund before any freeze happens (G-04 And).
func TestRefundBlockedByIssuedInvoice(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	markVerified(t, uid)
	order := payPlanOrder(t, svc, uid, "STANDARD")

	rec := call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": order.ID,
		"title_type":        titleTypePersonal,
		"title":             "张三",
		"receive_email":     "zhangsan@example.com",
	}, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("invoice apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var inv createInvoiceResponse
	decodeBody(t, rec, &inv)
	if err := svc.IssueInvoice(context.Background(), inv.Invoice.ID, 900003, "https://invoice.example.com/1.pdf"); err != nil {
		t.Fatalf("issue: %v", err)
	}

	rec = call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("refund on issued invoice status=%d body=%s", rec.Code, rec.Body.String())
	}
	eb := errorBody(t, rec)
	if !strings.Contains(eb.Error.Message, "已开票") {
		t.Fatalf("message = %q, want 已开票 refusal", eb.Error.Message)
	}
	if got := frozenOf(t, uid); got != 0 {
		t.Fatalf("frozen = %d, want 0 (refusal must not freeze)", got)
	}
}

// The exported transitions reject an unreachable state instead of silently
// rewriting the row.
func TestRefundStateMachineGuards(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	consumeG04(t, svc, uid)
	rec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	var resp createRefundResponse
	decodeBody(t, rec, &resp)

	if err := svc.StartRefundPayout(context.Background(), resp.Refund.ID); !errors.Is(err, ErrRefundState) {
		t.Fatalf("APPLIED -> PAYING error = %v, want ErrRefundState", err)
	}
	if err := svc.ApproveRefund(context.Background(), resp.Refund.ID, uid); !errors.Is(err, ErrSelfAudit) {
		t.Fatalf("self-audit error = %v, want ErrSelfAudit", err)
	}
	if st := refundStateOf(t, resp.Refund.ID); st != RefundStateApplied {
		t.Fatalf("state after refused self-audit = %q, want APPLIED", st)
	}
	var guardRows int
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE operator_id = ? AND action = 'APPROVE' AND result = 'REJECTED'`,
		uid).Scan(&guardRows); err != nil {
		t.Fatal(err)
	}
	if guardRows != 1 {
		t.Fatalf("guarded audit rows = %d, want 1", guardRows)
	}
}
