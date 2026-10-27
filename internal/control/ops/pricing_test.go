package ops

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func newPricingModel(t *testing.T, svc *Service, uid int64, modelType string) modelView {
	t.Helper()
	return createTestModel(t, svc, uid, map[string]any{
		"model_name":   "t15-price-" + uniqueSuffix(),
		"display_name": "T15 定价模型",
		"region":       "DOMESTIC",
		"model_type":   modelType,
		"protocol_id":  protocolID(t, "OPENAI_COMPAT"),
	})
}

func priceBody(modelID int64, in, out, cost int64) map[string]any {
	return map[string]any{
		"model_id":              modelID,
		"in_price_micro_per_k":  in,
		"out_price_micro_per_k": out,
		"cost_in_micro_per_k":   cost,
		"cost_out_micro_per_k":  cost,
	}
}

// A8: a zero or negative unit price is a body error and the message names the
// exact field.
func TestPriceRejectsNonPositiveUnitPrice(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	cases := []struct {
		name    string
		in, out int64
		field   string
	}{
		{"zero input", 0, 1000, "in_price_micro_per_k"},
		{"negative input", -1, 1000, "in_price_micro_per_k"},
		{"zero output", 1000, 0, "out_price_micro_per_k"},
		{"negative output", 1000, -5, "out_price_micro_per_k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
				priceBody(model.ID, tc.in, tc.out, 100), testCallOptions{uid: uid, role: "OPS"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			body := errorBody(t, rec)
			if body.Error.Code != "invalid_request_body" {
				t.Fatalf("code=%q, want invalid_request_body", body.Error.Code)
			}
			if !strings.Contains(body.Error.Message, tc.field) {
				t.Fatalf("message %q does not name %s", body.Error.Message, tc.field)
			}
		})
	}
}

// A11: P=1000, r=8500, C=800 must show 850 < 1067; C=600 passes.
func TestMarginFloorExactValues(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
		priceBody(model.ID, 1000, 1000, 800), testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cost=800 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	refusal := errorBody(t, rec)
	if !strings.Contains(refusal.Error.Message, "850 < 1067") {
		t.Fatalf("margin message = %q, want the exact shortfall 850 < 1067", refusal.Error.Message)
	}
	if !strings.Contains(refusal.Error.Message, "4/3") {
		t.Fatalf("margin message must state the 4/3 floor: %q", refusal.Error.Message)
	}
	if !strings.Contains(refusal.Error.Message, "contract") {
		t.Fatalf("margin message must name the failing group: %q", refusal.Error.Message)
	}
	t.Logf("A11 cost=800 → HTTP 400: %s", refusal.Error.Message)

	rec = call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
		priceBody(model.ID, 1000, 1000, 600), testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("cost=600 status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
	var ok priceDetailResponse
	decodeBody(t, rec, &ok)
	t.Logf("A11 cost=600 → HTTP 201: rule=%d in=%d out=%d", ok.Price.ID,
		ok.Price.InPriceMicroPerK, ok.Price.OutPriceMicroPerK)
}

// The floor arithmetic is integer-only. Reading the source is a crude but
// direct guard against someone reintroducing a float constant.
func TestPricingSourceHasNoFloatingPoint(t *testing.T) {
	rawBytes, err := os.ReadFile("pricing.go")
	raw := string(rawBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"float32", "float64", "1.25", "1.333", "* 4 / 3"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("pricing.go contains banned money arithmetic %q", banned)
		}
	}
}

// A10: an increase 3 days out is refused; a decrease may take effect now.
func TestPriceNoticePeriod(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
		priceBody(model.ID, 1000, 1000, 600), testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("initial price status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created priceDetailResponse
	decodeBody(t, rec, &created)

	soon := time.Now().UTC().Add(3 * 24 * time.Hour)
	raise := priceBody(model.ID, 1000, 2000, 600)
	delete(raise, "model_id")
	raise["effective_from"] = soon
	rec = call(t, svc.UpdatePrice, http.MethodPatch,
		fmt.Sprintf("/api/admin/prices/%d", created.Price.ID), raise,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(created.Price.ID)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("raise 3 days out status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if !strings.Contains(body.Error.Message, "涨价需提前 7 天公示") {
		t.Fatalf("raise message = %q, want the 7-day notice copy", body.Error.Message)
	}
	if !strings.Contains(body.Error.Message, "最早可生效时间") {
		t.Fatalf("raise message must give the earliest effective time: %q", body.Error.Message)
	}
	t.Logf("A10 increase effective_from=%s → HTTP 400: %s",
		soon.Format(time.RFC3339), body.Error.Message)

	// A decrease is immediate. No effective_from is sent, so the server keeps
	// the row's existing (already reached) start time.
	// 950 keeps the contract-group floor (807 >= 800) while still decreasing.
	cut := priceBody(model.ID, 1000, 950, 600)
	delete(cut, "model_id")
	rec = call(t, svc.UpdatePrice, http.MethodPatch,
		fmt.Sprintf("/api/admin/prices/%d", created.Price.ID), cut,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(created.Price.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("decrease status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	t.Logf("A10 decrease 1000→950 → HTTP 200 (immediate)")

	// An increase eight days out is accepted and closes the old row at the new
	// start (the changeover is not an A12 conflict).
	future := priceBody(model.ID, 1000, 2000, 600)
	future["effective_from"] = time.Now().UTC().Add(8 * 24 * time.Hour)
	rec = call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices", future,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("raise 8 days out status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
	var scheduled priceDetailResponse
	decodeBody(t, rec, &scheduled)
	t.Logf("A10 increase 8 days out → HTTP 201 (rule %d, version %d)",
		scheduled.Price.ID, scheduled.Price.Version)
}

// A12: two overlapping enabled rows with different prices are refused, and the
// message names both sources (table + row id).
func TestPriceUniqueTruthSource(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
		priceBody(model.ID, 1000, 1000, 600), testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first price status=%d body=%s", rec.Code, rec.Body.String())
	}
	var first priceDetailResponse
	decodeBody(t, rec, &first)

	// A second enabled row overlaps the first with a different price. It is
	// written directly so the conflict is exercised at save time, not only in
	// the writer that would normally have prevented it.
	secondID := insertRawPriceRule(t, model.ID, 1200, time.Now().UTC().Add(-time.Hour))

	submitted := priceBody(model.ID, 1100, 1100, 600)
	submitted["confirm_no_cost"] = true
	rec = call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
		submitted, testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if body.Error.Code != "price_conflict" {
		t.Fatalf("code=%q, want price_conflict", body.Error.Code)
	}
	wantA := fmt.Sprintf("t_price_rule#%d", first.Price.ID)
	wantB := fmt.Sprintf("t_price_rule#%d", secondID)
	if !strings.Contains(body.Error.Message, wantA) || !strings.Contains(body.Error.Message, wantB) {
		t.Fatalf("message %q must name both sources %s and %s", body.Error.Message, wantA, wantB)
	}
	t.Logf("A12 HTTP 409: %s", body.Error.Message)
}

// F16-5 G-06: a missing cost is a warning plus a second confirmation, never a
// silent listing.
func TestMissingCostRequiresConfirmation(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	body := map[string]any{
		"model_id":              model.ID,
		"in_price_micro_per_k":  1000,
		"out_price_micro_per_k": 1000,
	}
	rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("without confirm status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	refusal := errorBody(t, rec)
	if !strings.Contains(refusal.Error.Message, "confirm_no_cost=true") {
		t.Fatalf("message must name the confirmation field: %q", refusal.Error.Message)
	}

	body["confirm_no_cost"] = true
	rec = call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("with confirm status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
}

func TestPriceUnknownTargetIs404(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	rec := call(t, svc.UpdatePrice, http.MethodPatch, "/api/admin/prices/999999",
		map[string]any{"in_price_micro_per_k": 1000},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": "999999"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if code := errorBody(t, rec).Error.Code; code != "resource_not_found" {
		t.Fatalf("code=%q, want resource_not_found", code)
	}
}

// Step 0: the admin model detail now answers 404 resource_not_found; the
// channel detail keeps its more specific channel_not_found.
func TestAdminDetailUses404Codes(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")

	rec := call(t, svc.GetModel, http.MethodGet, "/api/admin/models/999999", nil,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": "999999"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("model detail status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if code := errorBody(t, rec).Error.Code; code != "resource_not_found" {
		t.Fatalf("model detail code=%q, want resource_not_found", code)
	}
	t.Logf("model detail 999999 → HTTP 404 resource_not_found")

	rec = call(t, svc.GetChannel, http.MethodGet, "/api/admin/channels/999999", nil,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": "999999"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("channel detail status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if code := errorBody(t, rec).Error.Code; code != "channel_not_found" {
		t.Fatalf("channel detail code=%q, want channel_not_found", code)
	}
	t.Logf("channel detail 999999 → HTTP 404 channel_not_found")
}

// insertRawPriceRule bypasses CreatePrice to plant a row the validator must
// detect. It returns the row id.
func insertRawPriceRule(t *testing.T, modelID, price int64, from time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_price_rule
			(scope_level, scope_id, model_type, in_price_micro_per_k, out_price_micro_per_k,
			 per_call_price_micro, currency_cost, markup_factor_bp, version,
			 effective_from, effective_to, enabled, created_by, created_at)
		VALUES ('MODEL', ?, 'TEXT', ?, ?, 0, 'CNY', 10000, 2, ?, NULL, 1, 0, ?)`,
		modelID, price, price, from, time.Now().UTC())
	if err != nil {
		t.Fatalf("insert raw price rule: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
