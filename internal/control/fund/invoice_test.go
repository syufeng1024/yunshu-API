package fund

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A5 / G-07: no real name → 403; over-amount → refusal with the remaining
// amount; a second application after ISSUED → refusal.
func TestA5InvoiceGatesAndIssuedLock(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	order := payPlanOrder(t, svc, uid, "STANDARD")

	body := map[string]any{
		"recharge_order_id": order.ID,
		"title_type":        titleTypePersonal,
		"title":             "张三",
		"receive_email":     "zhangsan@example.com",
	}

	// 1. Unverified account.
	rec := call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", body, uid)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unverified status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := errorBody(t, rec).Error.Code; code != "account_not_verified" {
		t.Fatalf("unverified code=%q, want account_not_verified", code)
	}
	t.Logf("unverified invoice: HTTP %d code=%s", rec.Code, errorBody(t, rec).Error.Code)

	markVerified(t, uid)

	// 2. Verified: the whole order amount is the default.
	rec = call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", body, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("verified status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created createInvoiceResponse
	decodeBody(t, rec, &created)
	if created.Invoice.AmountFen != 9900 || created.Invoice.State != InvoiceStateApplied {
		t.Fatalf("invoice = %+v, want 9900 fen / APPLIED", created.Invoice)
	}
	if created.Invoice.TaxRateBP != 10000 {
		t.Fatalf("tax_rate_bp = %d, want 10000 (1.00, Q-03)", created.Invoice.TaxRateBP)
	}

	// 3. A second application while the first is in flight.
	rec = call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", body, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate status=%d body=%s", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "处理中") {
		t.Fatalf("duplicate message=%q, want 处理中", msg)
	}
	t.Logf("duplicate application refusal: HTTP %d %s", rec.Code, errorBody(t, rec).Error.Message)

	// 4. Issue it, then refuse both a new application and an over-amount one.
	if err := svc.IssueInvoice(context.Background(), created.Invoice.ID, 900011,
		"https://invoice.example.com/t13.pdf"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	var invoiced int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT invoiced_fen FROM t_recharge_order WHERE id = ?`, order.ID).Scan(&invoiced); err != nil {
		t.Fatal(err)
	}
	if invoiced != 9900 {
		t.Fatalf("invoiced_fen = %d, want 9900", invoiced)
	}
	if n := countNotifications(t, uid, eventInvoiceProgress); n != 1 {
		t.Fatalf("INVOICE_PROGRESS notifications = %d, want 1", n)
	}

	rec = call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", body, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("after ISSUED status=%d body=%s", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "已开具") {
		t.Fatalf("after ISSUED message=%q, want 已开具", msg)
	}
	t.Logf("after ISSUED refusal: HTTP %d %s", rec.Code, errorBody(t, rec).Error.Message)
	t.Logf("issued: invoice_id=%d amount_fen=%d invoiced_fen=%d state=%s",
		created.Invoice.ID, created.Invoice.AmountFen, invoiced, created.Invoice.State)

	// 5. A fresh order: ask for more than the paid amount.
	order2 := payPlanOrder(t, svc, uid, "STANDARD")
	over := map[string]any{
		"recharge_order_id": order2.ID,
		"title_type":        titleTypePersonal,
		"title":             "张三",
		"receive_email":     "zhangsan@example.com",
		"amount_fen":        10000,
	}
	rec = call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", over, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-amount status=%d body=%s", rec.Code, rec.Body.String())
	}
	msg := errorBody(t, rec).Error.Message
	if !strings.Contains(msg, "剩余可开 9900 分") {
		t.Fatalf("over-amount message=%q, want remaining quota 9900 分", msg)
	}
	t.Logf("over-amount refusal: %s", msg)
}

// COMPANY requires a tax number; PERSONAL does not.
func TestInvoiceCompanyRequiresTaxNo(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	markVerified(t, uid)
	order := payPlanOrder(t, svc, uid, "STANDARD")

	rec := call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": order.ID,
		"title_type":        titleTypeCompany,
		"title":             "某某科技有限公司",
		"receive_email":     "finance@example.com",
	}, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tax_no status=%d body=%s", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "tax_no") {
		t.Fatalf("message=%q, want tax_no", msg)
	}

	rec = call(t, svc.CreateInvoice, http.MethodPost, "/api/console/invoices", map[string]any{
		"recharge_order_id": order.ID,
		"title_type":        titleTypeCompany,
		"title":             "某某科技有限公司",
		"tax_no":            "91310000MA1K3XYZ12",
		"receive_email":     "finance@example.com",
	}, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("with tax_no status=%d body=%s", rec.Code, rec.Body.String())
	}
}
