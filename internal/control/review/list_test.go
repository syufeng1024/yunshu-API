package review

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// A6 / F20-1: the default tab is PENDING and the order is created_at ASC.
// Insertion order is deliberately the reverse of submission order.
func TestReviewListDefaultsToPendingAscending(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	q1 := seedQuotaApply(t, applicant, 1_500_000, testBase.Add(-3*time.Hour))
	q2 := seedQuotaApply(t, applicant, 2_000_000, testBase.Add(-2*time.Hour))
	q3 := seedQuotaApply(t, applicant, 2_500_000, testBase.Add(-1*time.Hour))

	r3 := seedReviewTask(t, BizQuotaApply, q3, applicant, StatePending, testBase.Add(-1*time.Hour))
	r1 := seedReviewTask(t, BizQuotaApply, q1, applicant, StatePending, testBase.Add(-3*time.Hour))
	r2 := seedReviewTask(t, BizQuotaApply, q2, applicant, StatePending, testBase.Add(-2*time.Hour))

	// A terminal row that must not appear on the default PENDING tab.
	q4 := seedQuotaApply(t, applicant, 3_000_000, testBase.Add(-4*time.Hour))
	r4 := seedReviewTask(t, BizQuotaApply, q4, applicant, StateApproved, testBase.Add(-4*time.Hour))

	rec := callReview(t, svc.List, http.MethodGet,
		"/api/admin/reviews?biz_type=QUOTA_APPLY&page_size=100", "", nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp reviewListResponse
	decodeBody(t, rec, &resp)

	pos := make(map[int64]int, len(resp.Items))
	for i, item := range resp.Items {
		pos[item.ID] = i
	}
	for _, id := range []int64{r1, r2, r3} {
		if _, ok := pos[id]; !ok {
			t.Fatalf("review %d missing from default PENDING list: %s", id, rec.Body.String())
		}
	}
	if !(pos[r1] < pos[r2] && pos[r2] < pos[r3]) {
		t.Fatalf("order r1=%d r2=%d r3=%d, want created_at ASC", pos[r1], pos[r2], pos[r3])
	}
	if _, ok := pos[r4]; ok {
		t.Fatalf("APPROVED review %d appeared on the default PENDING tab", r4)
	}
	for i := 1; i < len(resp.Items); i++ {
		if resp.Items[i].CreatedAt.Before(resp.Items[i-1].CreatedAt) {
			t.Fatalf("items not ascending: [%d]=%s before [%d]=%s",
				i-1, resp.Items[i-1].CreatedAt, i, resp.Items[i].CreatedAt)
		}
	}
	for _, id := range []int64{r1, r2, r3} {
		item := resp.Items[pos[id]]
		t.Logf("A6 pending id=%d created_at=%s state=%s amount_micro=%v",
			item.ID, item.CreatedAt.Format(time.RFC3339), item.State, *item.AmountMicro)
	}
	t.Logf("A6 default tab=PENDING; approved %d absent; order r1(%d)<r2(%d)<r3(%d)",
		r4, pos[r1], pos[r2], pos[r3])
	if resp.Items[pos[r1]].ApplicantName == "" {
		t.Fatal("applicant name is empty; the list must carry it for the operator")
	}
	if resp.Items[pos[r1]].AmountMicro == nil || *resp.Items[pos[r1]].AmountMicro != 1_500_000 {
		t.Fatalf("amount_micro=%v, want 1500000", resp.Items[pos[r1]].AmountMicro)
	}
}

// A6 (filtered): type/state filters keep the same ascending order.
func TestReviewListFiltersKeepAscendingOrder(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	qEarly := seedQuotaApply(t, applicant, 1_500_000, testBase.Add(-5*time.Hour))
	qLate := seedQuotaApply(t, applicant, 1_800_000, testBase.Add(-1*time.Hour))
	rLate := seedReviewTask(t, BizQuotaApply, qLate, applicant, StatePending, testBase.Add(-1*time.Hour))
	rEarly := seedReviewTask(t, BizQuotaApply, qEarly, applicant, StatePending, testBase.Add(-5*time.Hour))

	refundID := seedRefund(t, applicant, testBase.Add(-2*time.Hour))
	refundReview := seedReviewTask(t, BizRefundApply, refundID, applicant, StatePending, testBase.Add(-2*time.Hour))

	rec := callReview(t, svc.List, http.MethodGet,
		"/api/admin/reviews?biz_type=QUOTA_APPLY&state=PENDING&page_size=100", "", nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp reviewListResponse
	decodeBody(t, rec, &resp)
	pos := make(map[int64]int, len(resp.Items))
	for i, item := range resp.Items {
		if item.BizType != BizQuotaApply {
			t.Fatalf("biz_type=%s leaked into a QUOTA_APPLY filter", item.BizType)
		}
		pos[item.ID] = i
	}
	if !(pos[rEarly] < pos[rLate]) {
		t.Fatalf("filtered order early=%d late=%d, want ascending", pos[rEarly], pos[rLate])
	}
	if _, ok := pos[refundReview]; ok {
		t.Fatal("refund review leaked into the quota filter")
	}

	rec = callReview(t, svc.List, http.MethodGet,
		"/api/admin/reviews?biz_type=REFUND_APPLY&page_size=100", "", nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund list status=%d body=%s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &resp)
	found := false
	for _, item := range resp.Items {
		if item.BizType != BizRefundApply {
			t.Fatalf("biz_type=%s leaked into a REFUND_APPLY filter", item.BizType)
		}
		if item.ID == refundReview {
			found = true
			if item.BizNo == "" || item.AmountMicro == nil || item.AmountFen == nil {
				t.Fatalf("refund enrichment incomplete: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("refund review %d missing from its own filter", refundReview)
	}
	t.Logf("A6 filtered QUOTA_APPLY: rEarly(%d) < rLate(%d); refund %d excluded",
		pos[rEarly], pos[rLate], refundReview)
	t.Logf("A6 filtered REFUND_APPLY: %d present with biz_no/amounts enriched", refundReview)
}

// A6 (anomaly): a queue row whose business row is gone is flagged, not a 500.
func TestReviewListFlagsOrphanedBusinessRow(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	reviewer := newTestUser(t, "FINANCE")

	orphan := seedReviewTask(t, BizQuotaApply, 9_876_543_210, applicant, StatePending,
		testBase.Add(-time.Hour))

	rec := callReview(t, svc.List, http.MethodGet,
		"/api/admin/reviews?biz_type=QUOTA_APPLY&page_size=100", "", nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("orphan list status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var resp reviewListResponse
	decodeBody(t, rec, &resp)
	for _, item := range resp.Items {
		if item.ID == orphan {
			if !item.Anomaly || item.AnomalyReason == nil || *item.AnomalyReason == "" {
				t.Fatalf("orphan item not flagged: %+v", item)
			}
			return
		}
	}
	t.Fatalf("orphan review %d missing from list", orphan)
}

// seedRefund inserts one APPLIED refund row so the list can join it.
func seedRefund(t *testing.T, uid int64, createdAt time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_refund_order
			(refund_no, user_id, recharge_order_id, apply_micro, refund_micro,
			 refund_fen, fee_fen, state, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?, 'APPLIED', ?,?)`,
		fmt.Sprintf("RF%d%06d", createdAt.UnixNano()%1e14, uid%1000000),
		uid, 1, 1_000_000, 900_000, 9000, 1000, createdAt, createdAt)
	if err != nil {
		t.Fatalf("seed refund: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("refund id: %v", err)
	}
	return id
}
