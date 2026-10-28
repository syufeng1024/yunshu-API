package review

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A17: the second approval answers 409 and every side effect happens once.
// The rate-limit policy row is the side effect that would double if the
// PENDING claim were not atomic.
func TestReviewApproveIsIdempotent(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	quotaID := seedQuotaApply(t, applicant, 1_500_000, testBase.Add(-time.Hour))
	reviewID := seedReviewTask(t, BizQuotaApply, quotaID, applicant, StatePending, testBase.Add(-time.Hour))

	rec := callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(reviewID), nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("first approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var first reviewDetailResponse
	decodeBody(t, rec, &first)
	if first.Review.State != StateApproved {
		t.Fatalf("state=%s, want APPROVED", first.Review.State)
	}
	firstPolicies := countQuotaPolicies(t, applicant)
	if firstPolicies != 1 {
		t.Fatalf("rate-limit policies=%d after first approve, want 1", firstPolicies)
	}
	state, _ := quotaState(t, quotaID)
	if state != "APPROVED" {
		t.Fatalf("t_quota_apply.state=%s, want APPROVED", state)
	}
	if n := countReviewAudit(t, reviewID, "APPROVE", "OK"); n != 1 {
		t.Fatalf("REVIEW/APPROVE audit rows=%d, want 1", n)
	}
	rState, decidedAt, _ := reviewState(t, reviewID)

	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(reviewID), nil, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second approve status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if eb := errorOf(t, rec); eb.Error.Code != "resource_state_conflict" {
		t.Fatalf("code=%q, want resource_state_conflict", eb.Error.Code)
	}
	if n := countQuotaPolicies(t, applicant); n != 1 {
		t.Fatalf("rate-limit policies=%d after duplicate approve, want 1", n)
	}
	if n := countReviewAudit(t, reviewID, "APPROVE", "OK"); n != 1 {
		t.Fatalf("REVIEW/APPROVE audit rows=%d after duplicate, want 1", n)
	}
	rState2, decidedAt2, _ := reviewState(t, reviewID)
	if rState2 != StateApproved || !decidedAt.Valid || !decidedAt2.Valid ||
		!decidedAt.Time.Equal(decidedAt2.Time) {
		t.Fatalf("review row changed on duplicate: state=%s decided=%v -> %s decided=%v",
			rState, decidedAt.Time, rState2, decidedAt2.Time)
	}

	// A rejection after a successful approval must not undo the approval.
	rec = callReview(t, svc.Reject, http.MethodPost,
		"/api/admin/reviews/1/reject", itoa(reviewID),
		map[string]any{"reason": "事后反悔"}, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("reject after approve status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	state, _ = quotaState(t, quotaID)
	if state != "APPROVED" {
		t.Fatalf("t_quota_apply.state=%s after rejected reversal, want APPROVED", state)
	}

	// The decision audit carries the before/after states, operator and time.
	var (
		operatorID int64
		before     []byte
		after      []byte
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT operator_id, before_json, after_json FROM t_admin_audit_log
		 WHERE module = 'REVIEW' AND action = 'APPROVE' AND result = 'OK'
		   AND target_id = ? ORDER BY id DESC LIMIT 1`, itoa(reviewID)).
		Scan(&operatorID, &before, &after); err != nil {
		t.Fatalf("read decision audit: %v", err)
	}
	if operatorID != reviewer {
		t.Fatalf("audit operator=%d, want %d", operatorID, reviewer)
	}
	if !strings.Contains(string(before), `"PENDING"`) ||
		!strings.Contains(string(after), `"APPROVED"`) ||
		!strings.Contains(string(after), `"biz_type": "QUOTA_APPLY"`) {
		t.Fatalf("before/after missing state or document: before=%s after=%s", before, after)
	}
	t.Logf("A17 first approve=200; quota policies=%d; duplicate approve=409 resource_state_conflict; policies still=%d; decided_at unchanged=%v",
		firstPolicies, countQuotaPolicies(t, applicant),
		decidedAt.Time.Equal(decidedAt2.Time))
	t.Logf("A17 audit operator=%d before=%s after=%s", operatorID, before, after)
}

// REFUND_APPLY approve delegates to fund.ApproveRefund (T13). Payout is a
// separate T13 step; this test only proves the queue hands the decision to the
// owner of the refund state machine.
func TestReviewApproveDispatchesRefund(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	refundID := seedRefund(t, applicant, testBase.Add(-time.Hour))
	reviewID := seedReviewTask(t, BizRefundApply, refundID, applicant, StatePending,
		testBase.Add(-time.Hour))

	rec := callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(reviewID), nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_refund_order WHERE id = ?`, refundID).Scan(&state); err != nil {
		t.Fatalf("refund state: %v", err)
	}
	if state != "APPROVED" {
		t.Fatalf("t_refund_order.state=%s, want APPROVED", state)
	}
	if rState, _, _ := reviewState(t, reviewID); rState != StateApproved {
		t.Fatalf("review state=%s, want APPROVED", rState)
	}
	t.Logf("REFUND_APPLY approve -> fund.ApproveRefund: t_refund_order.state=%s review.state=%s",
		state, StateApproved)
}

// A8: the applicant cannot decide their own document on either path.
func TestReviewSelfAuditRefusedOnApproveAndReject(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "FINANCE") // also the auditor in this test

	quotaID := seedQuotaApply(t, applicant, 1_500_000, testBase.Add(-time.Hour))
	reviewID := seedReviewTask(t, BizQuotaApply, quotaID, applicant, StatePending, testBase.Add(-time.Hour))

	rec := callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(reviewID), nil, applicant)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self approve status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	eb := errorOf(t, rec)
	if eb.Error.Code != "permission_denied" || !strings.Contains(eb.Error.Hint, "审批人不得为申请人") {
		t.Fatalf("self approve body=%+v, want permission_denied + hint", eb.Error)
	}

	rec = callReview(t, svc.Reject, http.MethodPost,
		"/api/admin/reviews/1/reject", itoa(reviewID),
		map[string]any{"reason": "自己驳回自己"}, applicant)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self reject status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	eb = errorOf(t, rec)
	if eb.Error.Code != "permission_denied" || !strings.Contains(eb.Error.Hint, "审批人不得为申请人") {
		t.Fatalf("self reject body=%+v, want permission_denied + hint", eb.Error)
	}

	state, _ := quotaState(t, quotaID)
	if state != "PENDING" {
		t.Fatalf("t_quota_apply.state=%s after two refusals, want PENDING", state)
	}
	rState, _, _ := reviewState(t, reviewID)
	if rState != StatePending {
		t.Fatalf("review state=%s after two refusals, want PENDING", rState)
	}
	if n := countReviewAudit(t, reviewID, "APPROVE", "REJECTED"); n != 1 {
		t.Fatalf("refused APPROVE audit rows=%d, want 1", n)
	}
	if n := countReviewAudit(t, reviewID, "REJECT", "REJECTED"); n != 1 {
		t.Fatalf("refused REJECT audit rows=%d, want 1", n)
	}
	t.Logf("A8 applicant=%d: approve -> 403 permission_denied (hint: 审批人不得为申请人); reject -> 403; t_quota_apply.state=%s review.state=%s; refused audit rows=2",
		applicant, state, rState)
}

// Document types with no exported owner method are refused with a readable
// conflict; the queue row is not flipped (T16 hard constraint 11).
func TestReviewUnsupportedDocumentTypesAreReported(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	invoiceReview := seedReviewTask(t, BizInvoiceApply, 424242, applicant, StatePending,
		testBase.Add(-time.Hour))
	rec := callReview(t, svc.Reject, http.MethodPost,
		"/api/admin/reviews/1/reject", itoa(invoiceReview),
		map[string]any{"reason": "抬头有误"}, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("invoice reject status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if msg := errorOf(t, rec).Error.Message; !strings.Contains(msg, "RejectInvoice") {
		t.Fatalf("invoice reject message=%q, want the missing method named", msg)
	}
	if rState, _, _ := reviewState(t, invoiceReview); rState != StatePending {
		t.Fatalf("invoice review state=%s, want PENDING", rState)
	}

	redeemReview := seedReviewTask(t, BizRedeemBatch, 7, applicant, StatePending,
		testBase.Add(-2*time.Hour))
	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(redeemReview), nil, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("redeem approve status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if rState, _, _ := reviewState(t, redeemReview); rState != StatePending {
		t.Fatalf("redeem review state=%s, want PENDING", rState)
	}

	// BALANCE_ADJUST now has an owner (review/balance.go). A queue row whose
	// business document is missing is a 404, not the old "not implemented" 409;
	// the supported path is covered by balance_test.go.
	adjustReview := seedReviewTask(t, BizBalanceAdjust, 8, applicant, StatePending,
		testBase.Add(-3*time.Hour))
	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(adjustReview), nil, reviewer)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("balance adjust approve status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}
