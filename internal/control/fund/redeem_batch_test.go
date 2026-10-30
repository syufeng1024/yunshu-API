package fund

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// C1: one generation call writes the batch and exactly `quantity` codes, all
// distinct, and the database holds only SHA256(plaintext) + a display prefix.
func TestCreateRedeemBatchGeneratesCodes(t *testing.T) {
	svc, _ := newTestService(t)
	operator := newTestUser(t, nil)

	const quantity = 25
	rec := callOperator(t, svc.CreateRedeemBatch, http.MethodPost,
		"/api/admin/redeem-codes/batch", map[string]any{
			"batch_name":     "国庆活动 2026",
			"face_value_fen": 10000,
			"quantity":       quantity,
			"credit_target":  redeemTargetBonus,
			"valid_days":     30,
		}, operator, "FINANCE", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create batch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp createRedeemBatchResponse
	decodeBody(t, rec, &resp)
	batchID := resp.Batch.ID
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_redeem_code WHERE batch_id = ?`, batchID)
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_redeem_batch WHERE id = ?`, batchID)
	})

	if len(resp.Codes) != quantity {
		t.Fatalf("codes = %d, want %d", len(resp.Codes), quantity)
	}
	if resp.Batch.GeneratedCount != quantity || resp.Batch.RedeemedCount != 0 {
		t.Fatalf("generated=%d redeemed=%d, want %d/0",
			resp.Batch.GeneratedCount, resp.Batch.RedeemedCount, quantity)
	}
	if resp.Batch.FaceValueYuan != 100 || resp.Batch.CreditTarget != redeemTargetBonus {
		t.Fatalf("batch = %d yuan / %q, want 100 / BONUS",
			resp.Batch.FaceValueYuan, resp.Batch.CreditTarget)
	}

	seen := make(map[string]bool, quantity)
	for _, code := range resp.Codes {
		if len(code) != redeemCodeLength {
			t.Fatalf("code %q length = %d, want %d", code, len(code), redeemCodeLength)
		}
		if code != strings.ToUpper(code) {
			t.Fatalf("code %q is not upper case; Redeem upper-cases its input", code)
		}
		for _, bad := range []string{"0", "O", "1", "I", "L"} {
			if strings.Contains(code, bad) {
				t.Fatalf("code %q contains the ambiguous symbol %q", code, bad)
			}
		}
		if seen[code] {
			t.Fatalf("duplicate code %q in one batch", code)
		}
		seen[code] = true
	}

	// Nothing recoverable is stored: only the hash and the six-char prefix.
	rows, err := testDB.QueryContext(context.Background(),
		`SELECT code_hash, code_prefix, status FROM t_redeem_code WHERE batch_id = ?`, batchID)
	if err != nil {
		t.Fatalf("read codes: %v", err)
	}
	defer rows.Close()
	stored := make(map[string]string, quantity)
	for rows.Next() {
		var hash, prefix, status string
		if err := rows.Scan(&hash, &prefix, &status); err != nil {
			t.Fatalf("scan code: %v", err)
		}
		if status != redeemUnused {
			t.Fatalf("status = %q, want UNUSED", status)
		}
		stored[hash] = prefix
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(stored) != quantity {
		t.Fatalf("stored codes = %d, want %d", len(stored), quantity)
	}
	for _, code := range resp.Codes {
		sum := sha256.Sum256([]byte(code))
		if _, ok := stored[hex.EncodeToString(sum[:])]; !ok {
			t.Fatalf("code %q has no stored hash", code)
		}
	}
	var plainCount int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_redeem_code
		  WHERE batch_id = ? AND (code_hash = ? OR code_prefix = ?)`,
		batchID, resp.Codes[0], resp.Codes[0]).Scan(&plainCount); err != nil {
		t.Fatalf("plaintext probe: %v", err)
	}
	if plainCount != 0 {
		t.Fatalf("a column holds the plaintext of %q", resp.Codes[0])
	}
	t.Logf("batch=%d quantity=%d first_code=%s", batchID, len(resp.Codes), resp.Codes[0])
}

// C2: the request bounds are enforced. A rejected generation writes nothing.
func TestCreateRedeemBatchValidation(t *testing.T) {
	svc, _ := newTestService(t)
	operator := newTestUser(t, nil)

	base := func() map[string]any {
		return map[string]any{
			"batch_name":     "validate",
			"face_value_fen": 5000,
			"quantity":       10,
			"credit_target":  redeemTargetBonus,
			"valid_days":     30,
		}
	}
	cases := []struct {
		name  string
		mutfn func(map[string]any)
		field string
	}{
		{"empty name", func(b map[string]any) { b["batch_name"] = "  " }, "batch_name"},
		{"name too long", func(b map[string]any) { b["batch_name"] = strings.Repeat("批", 65) }, "batch_name"},
		{"zero face value", func(b map[string]any) { b["face_value_fen"] = 0 }, "face_value_fen"},
		{"face value over smallint", func(b map[string]any) { b["face_value_fen"] = 65536 }, "face_value_fen"},
		{"quantity zero", func(b map[string]any) { b["quantity"] = 0 }, "quantity"},
		{"quantity over ceiling", func(b map[string]any) { b["quantity"] = 501 }, "quantity"},
		{"unknown credit target", func(b map[string]any) { b["credit_target"] = "CASH" }, "credit_target"},
		{"valid days zero", func(b map[string]any) { b["valid_days"] = 0 }, "valid_days"},
		{"valid days over ceiling", func(b map[string]any) { b["valid_days"] = 3651 }, "valid_days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := base()
			tc.mutfn(body)
			rec := callOperator(t, svc.CreateRedeemBatch, http.MethodPost,
				"/api/admin/redeem-codes/batch", body, operator, "FINANCE", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
			}
			got := errorBody(t, rec)
			if got.Error.Code != "invalid_request_body" {
				t.Fatalf("code = %q, want invalid_request_body", got.Error.Code)
			}
			if !strings.Contains(got.Error.Message, tc.field) {
				t.Fatalf("message = %q, want the field %q named", got.Error.Message, tc.field)
			}
		})
	}
}

// C3: end to end. A generated code redeems through the existing console
// endpoint: the balance grows and the batch's redeemed_count moves by one.
func TestGeneratedRedeemCodeRedeemsThroughConsole(t *testing.T) {
	svc, _ := newTestService(t)
	operator := newTestUser(t, nil)
	customer := newTestUser(t, nil)

	rec := callOperator(t, svc.CreateRedeemBatch, http.MethodPost,
		"/api/admin/redeem-codes/batch", map[string]any{
			"batch_name":     "e2e",
			"face_value_fen": 5000,
			"quantity":       1,
			"credit_target":  redeemTargetRecharge,
			"valid_days":     7,
		}, operator, "FINANCE", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create batch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created createRedeemBatchResponse
	decodeBody(t, rec, &created)
	batchID := created.Batch.ID
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_redeem_code WHERE batch_id = ?`, batchID)
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_redeem_batch WHERE id = ?`, batchID)
	})

	plain := created.Codes[0]
	// The customer types the code in lower case with padding: Redeem trims and
	// upper-cases, so a code that survived that round trip is usable as printed.
	rec = call(t, svc.Redeem, http.MethodPost, "/api/console/redeem",
		map[string]any{"code": "  " + strings.ToLower(plain) + " "}, customer)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", rec.Code, rec.Body.String())
	}
	var redeemed redeemResponse
	decodeBody(t, rec, &redeemed)
	wantMicro := fenToMicro(5000)
	if redeemed.AmountMicro != wantMicro {
		t.Fatalf("redeemed amount = %d, want %d", redeemed.AmountMicro, wantMicro)
	}

	b := balanceOf(t, customer)
	if b.Recharge != wantMicro || b.Total != wantMicro {
		t.Fatalf("balance = recharge %d total %d, want %d", b.Recharge, b.Total, wantMicro)
	}

	var redeemedCount int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT redeemed_count FROM t_redeem_batch WHERE id = ?`, batchID).Scan(&redeemedCount); err != nil {
		t.Fatalf("batch counter: %v", err)
	}
	if redeemedCount != 1 {
		t.Fatalf("redeemed_count = %d, want 1", redeemedCount)
	}

	var status string
	var ledgerID sql.NullInt64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT status, ledger_id FROM t_redeem_code WHERE batch_id = ?`, batchID).
		Scan(&status, &ledgerID); err != nil {
		t.Fatalf("code row: %v", err)
	}
	if status != redeemUsed || !ledgerID.Valid || ledgerID.Int64 == 0 {
		t.Fatalf("code = %q ledger=%v, want USED with a ledger id", status, ledgerID)
	}

	// A second attempt on the same code credits nothing.
	before := balanceOf(t, customer)
	rec = call(t, svc.Redeem, http.MethodPost, "/api/console/redeem",
		map[string]any{"code": plain}, customer)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second redeem status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if after := balanceOf(t, customer); after.Total != before.Total {
		t.Fatalf("balance after reuse = %d, want %d", after.Total, before.Total)
	}
	t.Logf("batch=%d code=%s balance=%d redeemed_count=%d", batchID, plain, b.Total, redeemedCount)
}

// C4: the batch list answers for finance.
func TestListRedeemBatches(t *testing.T) {
	svc, _ := newTestService(t)
	operator := newTestUser(t, nil)

	rec := callOperator(t, svc.CreateRedeemBatch, http.MethodPost,
		"/api/admin/redeem-codes/batch", map[string]any{
			"batch_name":     "列表批次",
			"face_value_fen": 1000,
			"quantity":       2,
			"credit_target":  redeemTargetBonus,
			"valid_days":     365,
		}, operator, "FINANCE", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create batch status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created createRedeemBatchResponse
	decodeBody(t, rec, &created)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_redeem_code WHERE batch_id = ?`, created.Batch.ID)
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_redeem_batch WHERE id = ?`, created.Batch.ID)
	})

	rec = callOperator(t, svc.ListRedeemBatches, http.MethodGet,
		"/api/admin/redeem-codes?page=1&page_size=100", nil, operator, "FINANCE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var list redeemBatchListResponse
	decodeBody(t, rec, &list)
	found := false
	for _, item := range list.Items {
		if item.ID != created.Batch.ID {
			continue
		}
		found = true
		if item.Quantity != 2 || item.GeneratedCount != 2 || item.RedeemedCount != 0 {
			t.Fatalf("item quantity=%d generated=%d redeemed=%d, want 2/2/0",
				item.Quantity, item.GeneratedCount, item.RedeemedCount)
		}
		if item.CreditTarget != redeemTargetBonus || item.FaceValueFen != 1000 {
			t.Fatalf("item target=%q face=%d, want BONUS/1000",
				item.CreditTarget, item.FaceValueFen)
		}
	}
	if !found {
		t.Fatalf("batch %d missing from the list (%d items)", created.Batch.ID, len(list.Items))
	}
}

// C5: the generator's alphabet and length stay inside what Redeem accepts.
// Redeem trims, upper-cases, then requires 6..64 characters; a generated code
// must therefore be 6..64 upper-case symbols, and the prefix column must be
// exactly six characters.
func TestRedeemCodeShapeMatchesRedeemContract(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := randomRedeemCode()
		if err != nil {
			t.Fatalf("randomRedeemCode: %v", err)
		}
		if len(code) < 6 || len(code) > 64 {
			t.Fatalf("code %q length %d outside Redeem's 6..64 window", code, len(code))
		}
		if strings.ToUpper(strings.TrimSpace(code)) != code {
			t.Fatalf("code %q would change under Redeem's normalisation", code)
		}
		for _, r := range code {
			if !strings.ContainsRune(redeemCodeCharset, r) {
				t.Fatalf("code %q contains %q outside the alphabet", code, r)
			}
		}
		if prefix := codePrefix(code); len(prefix) != 6 {
			t.Fatalf("prefix %q of %q is not six characters", prefix, code)
		}
	}
}
