package ops

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

// T40-c: the manual upstream bill import (MANUAL_UPLOAD). These tests use the
// real yunshu_test schema, like the rest of the package: the upsert semantics
// and the JSON column are exactly what they assert.

const importTestRequestID = "018f3c2a-0000-7000-8000-000000000001"

func billImportBody(channelID int64, period string, rows ...map[string]any) map[string]any {
	items := make([]map[string]any, 0, len(rows))
	items = append(items, rows...)
	return map[string]any{
		"channel_id":  channelID,
		"bill_period": period,
		"rows":        items,
	}
}

// deleteTestBills removes the rows the test channel's imports created. It runs
// before createTestChannel's channel cleanup (t.Cleanup is LIFO).
func deleteTestBills(t *testing.T, channelID int64) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_upstream_bill WHERE channel_id = ?`, channelID)
	})
}

func billCount(t *testing.T, channelID int64) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_upstream_bill WHERE channel_id = ?`, channelID).Scan(&n); err != nil {
		t.Fatalf("count bills: %v", err)
	}
	return n
}

func importBills(t *testing.T, svc *Service, uid int64, body map[string]any) upstreamBillImportResponse {
	t.Helper()
	rec := call(t, svc.ImportUpstreamBills, http.MethodPost, "/api/admin/upstream-bills/import",
		body, testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp upstreamBillImportResponse
	decodeBody(t, rec, &resp)
	return resp
}

// ① importing two rows writes two rows, keeps request_id NULL-able (the DAILY
// precision path and the approximate path both have to land), and stores the
// original row verbatim.
func TestUpstreamBillImportWritesRows(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	channel := createTestChannel(t, svc, uid, channelBody(t, "t40c-import-"+uniqueSuffix(), "DOMESTIC"))
	deleteTestBills(t, channel.ID)

	const period = "2026-10-25"
	body := billImportBody(channel.ID, period,
		map[string]any{
			"bill_no":           "TL40C-1",
			"request_id":        importTestRequestID,
			"upstream_trade_no": "TRADE-1",
			"model_name":        "qwen-max",
			"tokens_in":         1200,
			"tokens_out":        300,
			"amount_micro":      350000,
			// A column this endpoint does not know: it must survive in raw_row.
			"upstream_batch": "B-20261025",
		},
		map[string]any{
			"bill_no":      "TL40C-2",
			"model_name":   "qwen-plus",
			"tokens_in":    10,
			"tokens_out":   5,
			"amount_micro": 0,
		},
	)

	resp := importBills(t, svc, uid, body)
	if resp.Received != 2 || resp.Inserted != 2 || resp.Updated != 0 || resp.Unchanged != 0 {
		t.Fatalf("counts = %+v, want received=2 inserted=2 updated=0 unchanged=0", resp)
	}
	if n := billCount(t, channel.ID); n != 2 {
		t.Fatalf("rows in t_upstream_bill = %d, want 2", n)
	}

	var (
		adapter  string
		reqID    sql.NullString
		tradeNo  sql.NullString
		model    sql.NullString
		tin, to  int64
		amount   int64
		currency string
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT bill_adapter, request_id, upstream_trade_no, model_name,
		       tokens_in, tokens_out, amount_micro, currency
		  FROM t_upstream_bill
		 WHERE channel_id = ? AND bill_period = ? AND bill_no = 'TL40C-1'`,
		channel.ID, period).Scan(&adapter, &reqID, &tradeNo, &model, &tin, &to, &amount, &currency); err != nil {
		t.Fatalf("load TL40C-1: %v", err)
	}
	if adapter != billAdapterManualUpload {
		t.Fatalf("bill_adapter = %q, want %q", adapter, billAdapterManualUpload)
	}
	if !reqID.Valid || reqID.String != importTestRequestID {
		t.Fatalf("request_id = %+v, want %q", reqID, importTestRequestID)
	}
	if !tradeNo.Valid || tradeNo.String != "TRADE-1" {
		t.Fatalf("upstream_trade_no = %+v", tradeNo)
	}
	if !model.Valid || model.String != "qwen-max" {
		t.Fatalf("model_name = %+v", model)
	}
	if tin != 1200 || to != 300 || amount != 350000 || currency != "CNY" {
		t.Fatalf("tokens/amount/currency = %d/%d/%d/%q", tin, to, amount, currency)
	}

	// The row without request_id must store SQL NULL: runExternal's approximate
	// match (daily.go:937-951) keys off an empty request_id.
	var nullReq sql.NullString
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT request_id FROM t_upstream_bill
		  WHERE channel_id = ? AND bill_period = ? AND bill_no = 'TL40C-2'`,
		channel.ID, period).Scan(&nullReq); err != nil {
		t.Fatalf("load TL40C-2: %v", err)
	}
	if nullReq.Valid {
		t.Fatalf("request_id = %q, want NULL for a row imported without one", nullReq.String)
	}

	var batch string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT JSON_UNQUOTE(JSON_EXTRACT(raw_row, '$.upstream_batch'))
		  FROM t_upstream_bill
		 WHERE channel_id = ? AND bill_period = ? AND bill_no = 'TL40C-1'`,
		channel.ID, period).Scan(&batch); err != nil {
		t.Fatalf("raw_row upstream_batch: %v", err)
	}
	if batch != "B-20261025" {
		t.Fatalf("raw_row lost the upstream-only column: upstream_batch=%q", batch)
	}

	// Audit: operator, module/action and the imported-row counts.
	var (
		operatorID int64
		afterJSON  string
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT operator_id, after_json
		  FROM t_admin_audit_log
		 WHERE module = 'UPSTREAM_BILL' AND action = 'IMPORT' AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, channel.ID).Scan(&operatorID, &afterJSON); err != nil {
		t.Fatalf("import audit: %v", err)
	}
	if operatorID != uid {
		t.Fatalf("audit operator_id = %d, want %d", operatorID, uid)
	}
	var audited map[string]any
	if err := jsonx.Unmarshal([]byte(afterJSON), &audited); err != nil {
		t.Fatalf("audit after_json: %v", err)
	}
	if audited["received"] != float64(2) || audited["inserted"] != float64(2) ||
		audited["bill_period"] != period {
		t.Fatalf("audit after_json = %s", afterJSON)
	}

	t.Logf("imported %d rows: %d inserted, %d updated, %d unchanged; audit after=%s",
		resp.Received, resp.Inserted, resp.Updated, resp.Unchanged, afterJSON)
}

// ② importing the same batch twice does not add rows, and a corrected amount
// replaces the line instead of stacking a second copy.
func TestUpstreamBillImportIsIdempotent(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	channel := createTestChannel(t, svc, uid, channelBody(t, "t40c-idem-"+uniqueSuffix(), "DOMESTIC"))
	deleteTestBills(t, channel.ID)

	const period = "2026-10-26"
	row := func(amount int64) map[string]any {
		return map[string]any{
			"bill_no":      "TL40C-IDEM-1",
			"model_name":   "qwen-max",
			"tokens_in":    100,
			"tokens_out":   20,
			"amount_micro": amount,
		}
	}
	second := func() map[string]any {
		return map[string]any{
			"bill_no":      "TL40C-IDEM-2",
			"model_name":   "qwen-plus",
			"tokens_in":    7,
			"tokens_out":   3,
			"amount_micro": 900,
		}
	}

	first := importBills(t, svc, uid, billImportBody(channel.ID, period, row(350000), second()))
	if first.Inserted != 2 || billCount(t, channel.ID) != 2 {
		t.Fatalf("first import = %+v, rows=%d", first, billCount(t, channel.ID))
	}

	again := importBills(t, svc, uid, billImportBody(channel.ID, period, row(350000), second()))
	if again.Inserted != 0 || again.Unchanged != 2 {
		t.Fatalf("repeat import = %+v, want inserted=0 unchanged=2", again)
	}
	if n := billCount(t, channel.ID); n != 2 {
		t.Fatalf("rows after repeat import = %d, want 2", n)
	}

	// A provider correction arrives: same bill_no, different amount. The row is
	// updated in place, so runExternal sees one line and the corrected amount.
	corrected := importBills(t, svc, uid, billImportBody(channel.ID, period, row(360000), second()))
	if corrected.Inserted != 0 || corrected.Updated != 1 || corrected.Unchanged != 1 {
		t.Fatalf("corrected import = %+v, want inserted=0 updated=1 unchanged=1", corrected)
	}
	if n := billCount(t, channel.ID); n != 2 {
		t.Fatalf("rows after corrected import = %d, want 2", n)
	}
	var amount int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT amount_micro FROM t_upstream_bill
		  WHERE channel_id = ? AND bill_period = ? AND bill_no = 'TL40C-IDEM-1'`,
		channel.ID, period).Scan(&amount); err != nil {
		t.Fatalf("load corrected row: %v", err)
	}
	if amount != 360000 {
		t.Fatalf("amount after correction = %d, want 360000", amount)
	}

	t.Logf("idempotent: first=%+v repeat=%+v corrected=%+v rows=2", first, again, corrected)
}

// ③ malformed rows are refused with 400 invalid_request_body and write nothing.
// The float amount case is hard constraint D-07: micro-credits are integers.
func TestUpstreamBillImportRejectsInvalidRows(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	channel := createTestChannel(t, svc, uid, channelBody(t, "t40c-reject-"+uniqueSuffix(), "DOMESTIC"))
	deleteTestBills(t, channel.ID)

	validRow := func() map[string]any {
		return map[string]any{
			"bill_no": "TL40C-BAD", "amount_micro": 100, "tokens_in": 1, "tokens_out": 1,
		}
	}
	cases := []struct {
		name    string
		body    map[string]any
		wantMsg string
	}{
		{
			name:    "missing bill_no",
			body:    billImportBody(channel.ID, "2026-10-25", map[string]any{"amount_micro": 100}),
			wantMsg: "rows[0].bill_no",
		},
		{
			name: "float amount_micro",
			body: billImportBody(channel.ID, "2026-10-25",
				map[string]any{"bill_no": "TL40C-BAD", "amount_micro": 1.5}),
			wantMsg: "rows[0].amount_micro",
		},
		{
			name: "negative amount_micro",
			body: billImportBody(channel.ID, "2026-10-25",
				map[string]any{"bill_no": "TL40C-BAD", "amount_micro": -1}),
			wantMsg: "rows[0].amount_micro",
		},
		{
			name: "negative tokens_in",
			body: billImportBody(channel.ID, "2026-10-25",
				map[string]any{"bill_no": "TL40C-BAD", "amount_micro": 100, "tokens_in": -5}),
			wantMsg: "rows[0].tokens_in",
		},
		{
			name:    "month-only bill_period",
			body:    billImportBody(channel.ID, "2026-10", validRow()),
			wantMsg: "bill_period",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, svc.ImportUpstreamBills, http.MethodPost,
				"/api/admin/upstream-bills/import", tc.body,
				testCallOptions{uid: uid, role: "OPS"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			body := errorBody(t, rec)
			if body.Error.Code != "invalid_request_body" {
				t.Fatalf("code=%q, want invalid_request_body", body.Error.Code)
			}
			if !strings.Contains(body.Error.Message, tc.wantMsg) {
				t.Fatalf("message=%q, want it to name %q", body.Error.Message, tc.wantMsg)
			}
			if n := billCount(t, channel.ID); n != 0 {
				t.Fatalf("rejected import wrote %d rows", n)
			}
		})
	}

	// channel_id must reference a live channel; the answer is a body error the
	// operator can fix, not a 500.
	rec := call(t, svc.ImportUpstreamBills, http.MethodPost, "/api/admin/upstream-bills/import",
		billImportBody(987654321, "2026-10-25", validRow()), testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown channel status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if body.Error.Code != "invalid_request_body" || !strings.Contains(body.Error.Message, "channel_id") {
		t.Fatalf("unknown channel body=%+v", body.Error)
	}
	if n := billCount(t, 987654321); n != 0 {
		t.Fatalf("unknown channel import wrote %d rows", n)
	}
}

// ④ a role without channel.write is refused before the handler runs.
func TestUpstreamBillImportRequiresChannelWrite(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "PERSONAL")
	guarded := mw.Require(mw.PermChannelWrite)(http.HandlerFunc(svc.ImportUpstreamBills))

	body := billImportBody(1, "2026-10-25",
		map[string]any{"bill_no": "TL40C-DENY", "amount_micro": 1})
	for _, role := range []string{"PERSONAL", "FINANCE"} {
		rec := call(t, guarded.ServeHTTP, http.MethodPost, "/api/admin/upstream-bills/import",
			body, testCallOptions{uid: uid, role: role})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("role %s status=%d body=%s, want 403", role, rec.Code, rec.Body.String())
		}
		eb := errorBody(t, rec)
		if eb.Error.Code != "permission_denied" {
			t.Fatalf("role %s code=%q, want permission_denied", role, eb.Error.Code)
		}
	}

	// Positive control: OPS holds channel.write, so the guard lets the request
	// reach the handler. The body is missing bill_no, so the handler answers 400
	// without writing anything — proving the guard is not rejecting everyone.
	rec := call(t, guarded.ServeHTTP, http.MethodPost, "/api/admin/upstream-bills/import",
		billImportBody(1, "2026-10-25", map[string]any{"amount_micro": 1}),
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("OPS through guard status=%d body=%s, want 400 from the handler", rec.Code, rec.Body.String())
	}
}
