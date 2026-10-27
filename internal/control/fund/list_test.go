package fund

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"yunshu.dev/core/internal/ledger"
)

type failingMailer struct{ calls int }

func (m *failingMailer) Send(ctx context.Context, to, subject, body string) error {
	m.calls++
	return errors.New("smtp: connection refused")
}

// The three list endpoints share the page/filter plumbing; this test exercises
// every status branch and the invalid-filter and pagination exits.
func TestListEndpointsCoverAllFilters(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	markVerified(t, uid)
	order := payPlanOrder(t, svc, uid, "STANDARD")

	// One refund row.
	refundRec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	if refundRec.Code != http.StatusCreated {
		t.Fatalf("refund: %d %s", refundRec.Code, refundRec.Body.String())
	}
	// One invoice row on a second order.
	order2 := payPlanOrder(t, svc, uid, "STANDARD")
	invRec := call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": order2.ID,
		"title_type":        titleTypePersonal,
		"title":             "张三",
		"receive_email":     "zhangsan@example.com",
	}, uid)
	if invRec.Code != http.StatusCreated {
		t.Fatalf("invoice: %d %s", invRec.Code, invRec.Body.String())
	}
	// One quota row.
	current, _, _ := svc.currentDailyQuota(context.Background(), uid)
	qRec := call(t, svc.CreateQuotaApply, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": current * 2,
		"apply_reason":       "列表测试",
	}, uid)
	if qRec.Code != http.StatusCreated {
		t.Fatalf("quota: %d %s", qRec.Code, qRec.Body.String())
	}

	cases := []struct {
		name    string
		handler http.HandlerFunc
		base    string
		filters []string
		sorts   []string
		want    int
	}{
		{"refunds", svc.ListRefunds, "/api/console/refunds",
			[]string{"applied", "approved", "rejected", "paying", "paid", "failed", "all"},
			[]string{"created_at", "paid_at"}, 1},
		{"invoices", svc.ListInvoices, "/api/console/invoices",
			[]string{"applied", "approved", "issued", "rejected", "voided", "all"},
			[]string{"created_at", "issued_at"}, 1},
		{"quota", svc.ListQuotaApplies, "/api/console/quota-applies",
			[]string{"pending", "approved", "rejected", "expired", "all"},
			[]string{"created_at", "audited_at"}, 1},
	}
	for _, tc := range cases {
		for _, filter := range tc.filters {
			for _, sort := range tc.sorts {
				target := tc.base + "?status=" + filter + "&sort=" + sort + "&page=1&page_size=10"
				rec := call(t, tc.handler, http.MethodGet, target, nil, uid)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s status=%s sort=%s: %d %s", tc.name, filter, sort, rec.Code, rec.Body.String())
				}
				switch tc.name {
				case "refunds":
					var out refundListResponse
					decodeBody(t, rec, &out)
					if filter == "applied" && out.Total != 1 {
						t.Fatalf("refund applied total = %d, want 1", out.Total)
					}
					if filter != "applied" && filter != "all" && out.Total != 0 {
						t.Fatalf("refund %s total = %d, want 0", filter, out.Total)
					}
				case "invoices":
					var out invoiceListResponse
					decodeBody(t, rec, &out)
					if filter == "applied" && out.Total != 1 {
						t.Fatalf("invoice applied total = %d, want 1", out.Total)
					}
				case "quota":
					var out quotaListResponse
					decodeBody(t, rec, &out)
					if filter == "pending" && out.Total != 1 {
						t.Fatalf("quota pending total = %d, want 1", out.Total)
					}
				}
			}
		}
		// Unknown status and an over-limit page size must both be 400.
		bad := call(t, tc.handler, http.MethodGet, tc.base+"?status=nope", nil, uid)
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("%s unknown status = %d, want 400", tc.name, bad.Code)
		}
		bad = call(t, tc.handler, http.MethodGet, tc.base+"?page_size=101", nil, uid)
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("%s page_size=101 = %d, want 400", tc.name, bad.Code)
		}
	}
}

// RejectRefund releases the freeze without moving money, refuses self-audit,
// and needs a reason.
func TestRejectRefundReleasesFreeze(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	consumeG04(t, svc, uid)
	rec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	var created createRefundResponse
	decodeBody(t, rec, &created)
	before := balanceOf(t, uid)

	if err := svc.RejectRefund(context.Background(), created.Refund.ID, 900031, ""); !errors.Is(err, ledger.ErrInvalidParam) {
		t.Fatalf("empty reason error = %v, want invalid parameter", err)
	}
	if err := svc.RejectRefund(context.Background(), created.Refund.ID, uid, "自审"); !errors.Is(err, ErrSelfAudit) {
		t.Fatalf("self reject error = %v, want ErrSelfAudit", err)
	}
	if err := svc.RejectRefund(context.Background(), created.Refund.ID, 900031, "材料不完整"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if st := refundStateOf(t, created.Refund.ID); st != RefundStateRejected {
		t.Fatalf("state = %q, want REJECTED", st)
	}
	if got := frozenOf(t, uid); got != 0 {
		t.Fatalf("frozen = %d, want 0", got)
	}
	if after := balanceOf(t, uid); after != before {
		t.Fatalf("balance changed on reject: before=%+v after=%+v", before, after)
	}
	if st := orderState(t, order.ID); st != OrderStatePaid {
		t.Fatalf("recharge order state = %q, want PAID", st)
	}
}

// A failed e-mail must not roll back the money movement or the in-app row.
func TestNotifyEmailFailureDoesNotRollback(t *testing.T) {
	svc, _ := newTestService(t)
	mailer := &failingMailer{}
	svc.Mailer = mailer
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")
	consumeG04(t, svc, uid)

	rec := call(t, svc.CreateRefund, http.MethodPost, "/api/console/refunds",
		map[string]any{"recharge_order_id": order.ID}, uid)
	var created createRefundResponse
	decodeBody(t, rec, &created)
	if err := svc.ApproveRefund(context.Background(), created.Refund.ID, 900032); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartRefundPayout(context.Background(), created.Refund.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MarkRefundPaid(context.Background(), created.Refund.ID, "WXFAILMAIL"); err != nil {
		t.Fatalf("paid with failing mailer: %v", err)
	}
	if st := refundStateOf(t, created.Refund.ID); st != RefundStatePaid {
		t.Fatalf("state = %q, want PAID", st)
	}
	if mailer.calls != 1 {
		t.Fatalf("mailer calls = %d, want 1", mailer.calls)
	}
	var sendState string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT send_state FROM t_notification
		 WHERE user_id = ? AND event_code = ? ORDER BY id DESC LIMIT 1`,
		uid, eventRefundArrived).Scan(&sendState); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sendState, "FAILED:") {
		t.Fatalf("send_state = %s, want an email FAILED entry", sendState)
	}
}

// An account with no daily ceiling has nothing to raise: the free default is
// NULL, and the refusal must be explicit rather than a fabricated number.
func TestQuotaApplyRejectedWithoutDailyCeiling(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil) // no plan, no policy: FREE_DEFAULT has NULL quota
	rec := call(t, svc.CreateQuotaApply, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": 100_000_000,
		"apply_reason":       "无额度账户",
	}, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "没有日额度上限") {
		t.Fatalf("message=%q", msg)
	}
}
