package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/mw"
)

// A7 / F20-2: a rejection without a reason is a 400 from the server, and the
// accepted reason is visible at the applicant's own quota-apply list.
func TestReviewRejectRequiresReasonAndExposesIt(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	quotaID := seedQuotaApply(t, applicant, 1_500_000, testBase.Add(-time.Hour))
	reviewID := seedReviewTask(t, BizQuotaApply, quotaID, applicant, StatePending, testBase.Add(-time.Hour))

	for _, body := range []any{
		nil,
		map[string]any{},
		map[string]any{"reason": "   "},
		map[string]any{"reason": strings.Repeat("理", 256)},
	} {
		rec := callReview(t, svc.Reject, http.MethodPost,
			"/api/admin/reviews/1/reject", itoa(reviewID), body, reviewer)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("reject body=%v status=%d, want 400", body, rec.Code)
		}
		eb := errorOf(t, rec)
		if eb.Error.Code != "invalid_request_body" || !strings.Contains(eb.Error.Message, "reason") {
			t.Fatalf("reject body=%v error=%+v, want invalid_request_body naming reason", body, eb.Error)
		}
		t.Logf("A7 reject body=%v -> %d %s %q", body, rec.Code, eb.Error.Code, eb.Error.Message)
	}
	if rState, _, _ := reviewState(t, reviewID); rState != StatePending {
		t.Fatalf("review state=%s after rejected bodies, want PENDING", rState)
	}

	reason := "材料不足，请补充近三个月流水"
	rec := callReview(t, svc.Reject, http.MethodPost,
		"/api/admin/reviews/1/reject", itoa(reviewID), map[string]any{"reason": reason}, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp reviewDetailResponse
	decodeBody(t, rec, &resp)
	if resp.Review.State != StateRejected || resp.Review.DecisionNote == nil ||
		*resp.Review.DecisionNote != reason {
		t.Fatalf("response=%+v, want REJECTED with the reason", resp.Review)
	}

	state, rejectReason := quotaState(t, quotaID)
	if state != "REJECTED" || !rejectReason.Valid || rejectReason.String != reason {
		t.Fatalf("t_quota_apply state=%s reject_reason=%v, want REJECTED/%q",
			state, rejectReason, reason)
	}
	rState, _, note := reviewState(t, reviewID)
	if rState != StateRejected || !note.Valid || note.String != reason {
		t.Fatalf("review state=%s note=%v, want REJECTED/%q", rState, note, reason)
	}
	if n := countReviewAudit(t, reviewID, "REJECT", "OK"); n != 1 {
		t.Fatalf("REVIEW/REJECT audit rows=%d, want 1", n)
	}
	var auditReason string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT reason FROM t_admin_audit_log
		 WHERE module = 'REVIEW' AND action = 'REJECT' AND result = 'OK'
		   AND target_id = ? ORDER BY id DESC LIMIT 1`, itoa(reviewID)).Scan(&auditReason); err != nil {
		t.Fatalf("read reject audit: %v", err)
	}
	if auditReason != reason {
		t.Fatalf("audit reason=%q, want %q", auditReason, reason)
	}

	// The applicant sees the same reason at the console entry (F20-2).
	req := httptest.NewRequest(http.MethodGet, "/api/console/quota-applies?status=rejected", nil)
	req = req.WithContext(mw.WithSession(req.Context(), &mw.Session{UID: applicant, Role: "PERSONAL"}))
	userRec := httptest.NewRecorder()
	svc.Fund.ListQuotaApplies(userRec, req)
	if userRec.Code != http.StatusOK {
		t.Fatalf("applicant quota list status=%d body=%s", userRec.Code, userRec.Body.String())
	}
	var listed struct {
		Items []struct {
			ID           int64   `json:"id"`
			State        string  `json:"state"`
			RejectReason *string `json:"reject_reason"`
		} `json:"items"`
	}
	decodeBody(t, userRec, &listed)
	found := false
	for _, item := range listed.Items {
		if item.ID != quotaID {
			continue
		}
		found = true
		if item.State != "REJECTED" || item.RejectReason == nil || *item.RejectReason != reason {
			t.Fatalf("applicant sees state=%s reason=%v, want REJECTED/%q",
				item.State, item.RejectReason, reason)
		}
	}
	if !found {
		t.Fatalf("quota apply %d missing from the applicant's own list", quotaID)
	}
	t.Logf("A7 accepted reason=%q -> t_quota_apply.state=%s reject_reason=%q; applicant list shows state=%s reason=%q",
		reason, state, rejectReason.String, "REJECTED", reason)
}
