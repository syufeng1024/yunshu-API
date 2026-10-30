package fund

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

// callOperator invokes an operator handler with a synthetic session, the role
// that session carries and the path values the mux would normally supply.
// The route-level 403/200 matrix lives in package control (it needs the real
// middleware chain); these tests exercise the handler once it has been
// admitted.
func callOperator(t *testing.T, h http.HandlerFunc, method, target string, body any, uid int64, role string, pathValues map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = jsonx.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	for name, value := range pathValues {
		req.SetPathValue(name, value)
	}
	req = req.WithContext(mw.WithSession(req.Context(), &mw.Session{UID: uid, Role: role}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// confirmOrder posts one operator confirmation.
func confirmOrder(t *testing.T, svc *Service, operatorID int64, orderID int64, bankTxnNo, note string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"bank_txn_no": bankTxnNo}
	if note != "" {
		body["note"] = note
	}
	return callOperator(t, svc.ConfirmOrderPayment, http.MethodPost,
		"/api/admin/orders/"+itoa(orderID)+"/confirm-payment", body,
		operatorID, "FINANCE", map[string]string{"id": itoa(orderID)})
}

// orderPayTxn reads the transaction id and refundable ceiling a confirmation
// must have written.
func orderPayTxn(t *testing.T, orderID int64) (txn sql.NullString, refundable, recharge int64) {
	t.Helper()
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT pay_txn_id, refundable_micro, recharge_micro
		   FROM t_recharge_order WHERE id = ?`, orderID).
		Scan(&txn, &refundable, &recharge); err != nil {
		t.Fatalf("order pay txn: %v", err)
	}
	return txn, refundable, recharge
}

// auditRowCount counts operator confirmations for one order.
func auditRowCount(t *testing.T, operatorID, orderID int64) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_admin_audit_log
		  WHERE operator_id = ? AND module = 'FUND'
		    AND action = 'ORDER_CONFIRMED_BY_OPERATOR' AND target_id = ?`,
		operatorID, itoa(orderID)).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

// auditPayload returns the after_json of the newest confirmation row.
func auditPayload(t *testing.T, operatorID, orderID int64) string {
	t.Helper()
	var raw []byte
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT after_json FROM t_admin_audit_log
		  WHERE operator_id = ? AND action = 'ORDER_CONFIRMED_BY_OPERATOR' AND target_id = ?
		  ORDER BY id DESC LIMIT 1`, operatorID, itoa(orderID)).Scan(&raw); err != nil {
		t.Fatalf("audit payload: %v", err)
	}
	return string(raw)
}

// A1: an operator confirmation credits the account exactly like a channel
// callback would — balance, batches, ledger entries, order state and one audit
// row committed together.
func TestOperatorConfirmPaymentCreditsAndAudits(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	operator := newTestUser(t, nil)

	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":     OrderTypeCustom,
		"pay_amount_fen": 10000,
		"pay_channel":    PayChannelWechat,
	})
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance before confirmation = %d, want 0", b.Total)
	}

	const bankTxnNo = "20261030001"
	rec := confirmOrder(t, svc, operator, order.ID, bankTxnNo, "对公转账")
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp confirmPaymentResponse
	decodeBody(t, rec, &resp)
	if !resp.Credited || resp.Replayed {
		t.Fatalf("credited=%v replayed=%v, want true/false", resp.Credited, resp.Replayed)
	}
	if resp.Order.State != OrderStatePaid {
		t.Fatalf("response state = %q, want PAID", resp.Order.State)
	}

	if st := orderState(t, order.ID); st != OrderStatePaid {
		t.Fatalf("order state = %q, want PAID", st)
	}
	txn, refundable, recharge := orderPayTxn(t, order.ID)
	if !txn.Valid || txn.String != bankTxnNo {
		t.Fatalf("pay_txn_id = %q, want %q", txn.String, bankTxnNo)
	}
	if refundable != recharge || recharge != order.RechargeMicro {
		t.Fatalf("refundable=%d recharge=%d want %d", refundable, recharge, order.RechargeMicro)
	}

	b := balanceOf(t, uid)
	if b.Recharge != order.RechargeMicro || b.Total != order.RechargeMicro {
		t.Fatalf("balance = recharge %d total %d, want %d", b.Recharge, b.Total, order.RechargeMicro)
	}
	if got := countLedger(t, uid); got != 1 {
		t.Fatalf("ledger entries = %d, want 1", got)
	}
	if got := countBatches(t, uid); got != 1 {
		t.Fatalf("credit batches = %d, want 1", got)
	}
	if got := auditRowCount(t, operator, order.ID); got != 1 {
		t.Fatalf("audit rows = %d, want 1", got)
	}
	payload := auditPayload(t, operator, order.ID)
	for _, want := range []string{order.OrderNo, bankTxnNo, "对公转账"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("audit payload %s missing %q", payload, want)
		}
	}
	t.Logf("order=%s state=PAID balance=%d ledger=%d audit=%s",
		order.OrderNo, b.Total, countLedger(t, uid), payload)
}

// A2: the same statement number settles the same order once. A second
// confirmation is a replay (no credit), and a different number on a paid order
// is a conflict the operator has to reconcile by hand.
func TestOperatorConfirmPaymentIsIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	operator := newTestUser(t, nil)

	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":     OrderTypeCustom,
		"pay_amount_fen": 5000,
		"pay_channel":    PayChannelAlipay,
	})

	const bankTxnNo = "BANK-2026-0001"
	if rec := confirmOrder(t, svc, operator, order.ID, bankTxnNo, ""); rec.Code != http.StatusOK {
		t.Fatalf("first confirm status = %d body=%s", rec.Code, rec.Body.String())
	}
	firstBalance := balanceOf(t, uid)
	firstLedger := countLedger(t, uid)

	rec := confirmOrder(t, svc, operator, order.ID, bankTxnNo, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp confirmPaymentResponse
	decodeBody(t, rec, &resp)
	if !resp.Credited || !resp.Replayed {
		t.Fatalf("replay credited=%v replayed=%v, want true/true", resp.Credited, resp.Replayed)
	}
	if got := balanceOf(t, uid); got.Total != firstBalance.Total {
		t.Fatalf("balance after replay = %d, want %d (credited once)", got.Total, firstBalance.Total)
	}
	if got := countLedger(t, uid); got != firstLedger {
		t.Fatalf("ledger after replay = %d, want %d", got, firstLedger)
	}
	if got := auditRowCount(t, operator, order.ID); got != 1 {
		t.Fatalf("audit rows after replay = %d, want 1 (a replay writes nothing)", got)
	}

	// A different statement number on an already paid order is a mismatch: the
	// money may be a duplicate transfer, so it must be reconciled, not booked.
	rec = confirmOrder(t, svc, operator, order.ID, "BANK-2026-0002", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatch status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if body.Error.Code != "resource_state_conflict" {
		t.Fatalf("mismatch code = %q, want resource_state_conflict", body.Error.Code)
	}
	if got := balanceOf(t, uid); got.Total != firstBalance.Total {
		t.Fatalf("balance after mismatch = %d, want %d", got.Total, firstBalance.Total)
	}
}

// A3: a closed order cannot be settled. Recording money against it would
// create credit the pricing engine has already written off.
func TestOperatorConfirmPaymentRefusesNonPending(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	operator := newTestUser(t, nil)

	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":     OrderTypeCustom,
		"pay_amount_fen": 20000,
		"pay_channel":    PayChannelWechat,
	})
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_recharge_order SET state = 'CLOSED' WHERE id = ?`, order.ID); err != nil {
		t.Fatalf("close order: %v", err)
	}

	rec := confirmOrder(t, svc, operator, order.ID, "20261030009", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("closed confirm status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if body.Error.Code != "resource_state_conflict" {
		t.Fatalf("code = %q, want resource_state_conflict", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "CLOSED") {
		t.Fatalf("message = %q, want the blocking state named", body.Error.Message)
	}
	if st := orderState(t, order.ID); st != "CLOSED" {
		t.Fatalf("order state = %q, want CLOSED (unchanged)", st)
	}
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance = %d, want 0", b.Total)
	}
}

// A4: confirming a PLAN order also applies the plan, exactly as the callback
// does — the entitlement and the credits arrive in the same transaction.
func TestOperatorConfirmPaymentAppliesPlan(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	operator := newTestUser(t, nil)
	plan := planByCode(t, "STANDARD")

	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":  OrderTypePlan,
		"plan_id":     plan.ID,
		"pay_channel": PayChannelWechat,
	})

	rec := confirmOrder(t, svc, operator, order.ID, "PLAN-TXN-2026", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm plan status = %d body=%s", rec.Code, rec.Body.String())
	}

	b := balanceOf(t, uid)
	if b.Recharge != plan.GrantRechargeMicro || b.Bonus != plan.GrantBonusMicro {
		t.Fatalf("balance = recharge %d bonus %d, want %d / %d",
			b.Recharge, b.Bonus, plan.GrantRechargeMicro, plan.GrantBonusMicro)
	}
	if got := countBatches(t, uid); got != 2 {
		t.Fatalf("credit batches = %d, want 2 (recharge + bonus)", got)
	}

	var planID sql.NullInt64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT plan_id FROM t_user WHERE id = ?`, uid).Scan(&planID); err != nil {
		t.Fatalf("user plan: %v", err)
	}
	if !planID.Valid || planID.Int64 != plan.ID {
		t.Fatalf("user plan_id = %v, want %d", planID, plan.ID)
	}
	_, rpm, tpm, _, _ := activePolicy(t, uid)
	if rpm != plan.RPM || tpm != plan.TPM {
		t.Fatalf("rate-limit policy = rpm %d tpm %d, want %d / %d", rpm, tpm, plan.RPM, plan.TPM)
	}
}

// A5: the statement number is the operator's proof of payment, so a malformed
// one is a body error the operator fixes by retyping it.
func TestOperatorConfirmPaymentValidatesBankTxnNo(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	operator := newTestUser(t, nil)

	order, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":     OrderTypeCustom,
		"pay_amount_fen": 1000,
		"pay_channel":    PayChannelWechat,
	})

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing", map[string]any{"note": "x"}},
		{"too short", map[string]any{"bank_txn_no": "12345"}},
		{"illegal char", map[string]any{"bank_txn_no": "2026 1030 001"}},
		{"too long", map[string]any{"bank_txn_no": strings.Repeat("9", 65)}},
		{"note too long", map[string]any{"bank_txn_no": "20261030001", "note": strings.Repeat("说", confirmNoteMax+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callOperator(t, svc.ConfirmOrderPayment, http.MethodPost,
				"/api/admin/orders/"+itoa(order.ID)+"/confirm-payment", tc.body,
				operator, "FINANCE", map[string]string{"id": itoa(order.ID)})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if body := errorBody(t, rec); body.Error.Code != "invalid_request_body" {
				t.Fatalf("code = %q, want invalid_request_body", body.Error.Code)
			}
		})
	}
	if st := orderState(t, order.ID); st != OrderStatePending {
		t.Fatalf("order state = %q, want PENDING (nothing written)", st)
	}
	if b := balanceOf(t, uid); b.Total != 0 {
		t.Fatalf("balance = %d, want 0", b.Total)
	}
}

// A6: an unknown order id is a 404, not a body error — no edit to the body can
// make a missing row appear.
func TestOperatorConfirmPaymentUnknownOrder(t *testing.T) {
	svc, _ := newTestService(t)
	operator := newTestUser(t, nil)

	rec := confirmOrder(t, svc, operator, 999999999, "20261030001", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

// A7: the operator queue lists every account's orders with the identity
// finance needs, and the state filter and keyword search intersect.
func TestListAdminOrders(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	operator := newTestUser(t, nil)

	pending, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":     OrderTypeCustom,
		"pay_amount_fen": 3000,
		"pay_channel":    PayChannelWechat,
	})
	paid, _ := createOrder(t, svc, uid, map[string]any{
		"order_type":     OrderTypeCustom,
		"pay_amount_fen": 4000,
		"pay_channel":    PayChannelWechat,
	})
	if rec := confirmOrder(t, svc, operator, paid.ID, "LIST-TXN-0001", ""); rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d body=%s", rec.Code, rec.Body.String())
	}

	var email string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT email FROM t_user WHERE id = ?`, uid).Scan(&email); err != nil {
		t.Fatalf("user email: %v", err)
	}

	rec := callOperator(t, svc.ListAdminOrders, http.MethodGet,
		"/api/admin/orders?state=pending&q="+email, nil, operator, "FINANCE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp adminOrderListResponse
	decodeBody(t, rec, &resp)
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("total=%d items=%d, want 1/1", resp.Total, len(resp.Items))
	}
	item := resp.Items[0]
	if item.ID != pending.ID {
		t.Fatalf("item id = %d, want the pending order %d", item.ID, pending.ID)
	}
	if item.UserID != uid || item.UserEmail != email {
		t.Fatalf("identity = %d/%q, want %d/%q", item.UserID, item.UserEmail, uid, email)
	}
	if item.State != OrderStatePending || item.PayTxnID != "" {
		t.Fatalf("state=%q txn=%q, want PENDING/empty", item.State, item.PayTxnID)
	}

	// The paid order appears under state=paid with its statement number bound.
	rec = callOperator(t, svc.ListAdminOrders, http.MethodGet,
		"/api/admin/orders?state=paid&q="+email, nil, operator, "FINANCE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("paid list status = %d body=%s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &resp)
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("paid total=%d, want 1", resp.Total)
	}
	if got := resp.Items[0]; got.ID != paid.ID || got.PayTxnID != "LIST-TXN-0001" {
		t.Fatalf("paid item = %d/%q, want %d/%q", got.ID, got.PayTxnID, paid.ID, "LIST-TXN-0001")
	}
	if got := resp.Items[0].PayAmountYuan; got != 40 {
		t.Fatalf("pay_amount_yuan = %d, want 40", got)
	}

	// An unknown state filter is a query error, never "no filter".
	rec = callOperator(t, svc.ListAdminOrders, http.MethodGet,
		"/api/admin/orders?state=whatever", nil, operator, "FINANCE", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

// Guard: the shared bonus-days helper keeps the two settlement paths in step.
func TestOrderBonusDaysMatchesCallbackContract(t *testing.T) {
	svc, _ := newTestService(t)
	plan := planByCode(t, "STANDARD")

	tx, err := testDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only probe

	days, apiErr := svc.orderBonusDays(context.Background(), tx,
		sql.NullInt64{Int64: plan.ID, Valid: true}, plan.GrantBonusMicro)
	if apiErr != nil {
		t.Fatalf("orderBonusDays: %v", apiErr)
	}
	if days <= 0 {
		t.Fatalf("bonus days = %d, want a positive window", days)
	}
	zero, apiErr := svc.orderBonusDays(context.Background(), tx,
		sql.NullInt64{}, 0)
	if apiErr != nil || zero != 0 {
		t.Fatalf("no-bonus order days = %d err=%v, want 0", zero, apiErr)
	}
	t.Logf("plan bonus window = %d days", days)
}
