package ops

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func modelBody(t *testing.T, name, state string) map[string]any {
	t.Helper()
	return map[string]any{
		"model_name":    name,
		"display_name":  name + " 展示名",
		"region":        "DOMESTIC",
		"model_type":    "TEXT",
		"protocol_id":   protocolID(t, "OPENAI_COMPAT"),
		"listing_state": state,
	}
}

// F14-5 applies to model master data too.
func TestModelRejectsOverseasRegion(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	body := modelBody(t, "t15-model-overseas-"+uniqueSuffix(), "LISTED")
	body["region"] = "OVERSEAS"

	rec := call(t, svc.CreateModel, http.MethodPost, "/api/admin/models", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	eb := errorBody(t, rec)
	if eb.Error.Code != "invalid_request_body" || !strings.Contains(eb.Error.Message, "仅支持境内") {
		t.Fatalf("body=%+v, want an explicit domestic-only refusal", eb.Error)
	}
}

// F16-7 / F17-1: LISTED → CATALOG_ONLY needs no notice period, zeroes the
// recommendation weight and writes a draft; CATALOG_ONLY → LISTED writes a
// "new listing" draft. Both transitions are audited.
func TestModelListingTransitionsDraftAndZeroWeight(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	body := modelBody(t, "t15-model-state-"+uniqueSuffix(), "LISTED")
	body["recommend_weight"] = 7
	body["aliases"] = []string{"t15-alias-" + uniqueSuffix()}
	m := createTestModel(t, svc, uid, body)
	if m.RecommendWeight != 7 {
		t.Fatalf("initial weight=%d, want 7", m.RecommendWeight)
	}

	rec := call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "CATALOG_ONLY", "reason": "成本调整"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("to CATALOG_ONLY status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp modelDetailResponse
	decodeBody(t, rec, &resp)
	if resp.Model.ListingState != "CATALOG_ONLY" || resp.Model.RecommendWeight != 0 {
		t.Fatalf("state/weight=%s/%d, want CATALOG_ONLY/0", resp.Model.ListingState, resp.Model.RecommendWeight)
	}
	if resp.Model.PublishedAt == nil {
		t.Fatal("published_at was cleared by CATALOG_ONLY; the first listing date must be kept")
	}
	if n := draftCount(t, m.ModelName); n != 1 {
		t.Fatalf("drafts=%d after LISTED→CATALOG_ONLY, want 1", n)
	}
	assertListingAudit(t, m.ID, "成本调整")

	rec = call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "LISTED"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("to LISTED status=%d body=%s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &resp)
	if resp.Model.ListingState != "LISTED" {
		t.Fatalf("state=%s, want LISTED", resp.Model.ListingState)
	}
	if n := draftCount(t, m.ModelName); n != 2 {
		t.Fatalf("drafts=%d after CATALOG_ONLY→LISTED, want 2", n)
	}

	// Announcements are drafts and must never drive the model state back.
	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT listing_state FROM t_model WHERE id = ?`, m.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "LISTED" {
		t.Fatalf("model state=%s, want LISTED", state)
	}
}

// Hard constraint 11: CATALOG_ONLY / DELISTED models cannot hold a non-zero
// recommendation weight; moving off LISTED zeroes it automatically.
func TestModelRecommendWeightOnlyForListed(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")

	// Create-time refusal.
	body := modelBody(t, "t15-model-weight-"+uniqueSuffix(), "CATALOG_ONLY")
	body["recommend_weight"] = 5
	rec := call(t, svc.CreateModel, http.MethodPost, "/api/admin/models", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("create CATALOG_ONLY+weight status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if code := errorBody(t, rec).Error.Code; code != "resource_state_conflict" {
		t.Fatalf("code=%q, want resource_state_conflict", code)
	}

	// Set-endpoint refusal.
	ok := modelBody(t, "t15-model-weight-ok-"+uniqueSuffix(), "CATALOG_ONLY")
	m := createTestModel(t, svc, uid, ok)
	rec = call(t, svc.SetRecommendWeight, http.MethodPost, "/api/admin/models/1/recommend-weight",
		map[string]any{"recommend_weight": 10},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("set weight on CATALOG_ONLY status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "推荐权重必须为 0") {
		t.Fatalf("message=%q", msg)
	}

	// Zero is accepted, then the model is listed and a non-zero weight works.
	rec = call(t, svc.SetRecommendWeight, http.MethodPost, "/api/admin/models/1/recommend-weight",
		map[string]any{"recommend_weight": 0},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("set zero weight status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "LISTED"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.SetRecommendWeight, http.MethodPost, "/api/admin/models/1/recommend-weight",
		map[string]any{"recommend_weight": 10},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("set weight on LISTED status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp modelDetailResponse
	decodeBody(t, rec, &resp)
	if resp.Model.RecommendWeight != 10 {
		t.Fatalf("weight=%d, want 10", resp.Model.RecommendWeight)
	}

	// Moving back to CATALOG_ONLY zeroes it without an explicit weight field.
	rec = call(t, svc.UpdateModel, http.MethodPatch, "/api/admin/models/1",
		map[string]any{"listing_state": "CATALOG_ONLY"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch to CATALOG_ONLY status=%d body=%s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &resp)
	if resp.Model.RecommendWeight != 0 {
		t.Fatalf("weight=%d after leaving LISTED, want 0", resp.Model.RecommendWeight)
	}
}

func TestModelAliasConflict(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	alias := "t15-shared-alias-" + uniqueSuffix()

	first := modelBody(t, "t15-model-a-"+uniqueSuffix(), "LISTED")
	first["aliases"] = []string{alias}
	createTestModel(t, svc, uid, first)

	second := modelBody(t, "t15-model-b-"+uniqueSuffix(), "LISTED")
	second["aliases"] = []string{alias}
	rec := call(t, svc.CreateModel, http.MethodPost, "/api/admin/models", second,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate alias status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, alias) {
		t.Fatalf("message=%q, want the alias named", msg)
	}
}

func draftCount(t *testing.T, modelName string) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_announcement
		 WHERE state = 'DRAFT' AND content LIKE ?`, "%"+modelName+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertListingAudit(t *testing.T, modelID int64, reason string) {
	t.Helper()
	var (
		before, after []byte
		gotReason     *string
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT before_json, after_json, reason FROM t_admin_audit_log
		 WHERE module = 'MODEL' AND action = 'LISTING_STATE_CHANGE' AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, itoa(modelID)).Scan(&before, &after, &gotReason); err != nil {
		t.Fatal(err)
	}
	// MySQL normalises JSON columns with a space after the colon.
	if !strings.Contains(string(before), `"listing_state": "LISTED"`) {
		t.Fatalf("before_json=%s, want LISTED", before)
	}
	if !strings.Contains(string(after), `"listing_state": "CATALOG_ONLY"`) {
		t.Fatalf("after_json=%s, want CATALOG_ONLY", after)
	}
	if !strings.Contains(string(after), `"announcement_id"`) {
		t.Fatalf("after_json=%s, want the generated announcement id", after)
	}
	// The automatic zeroing is visible in the audit, not silent.
	if !strings.Contains(string(before), `"recommend_weight": 7`) ||
		!strings.Contains(string(after), `"recommend_weight": 0`) {
		t.Fatalf("before/after weight not recorded: before=%s after=%s", before, after)
	}
	if gotReason == nil || *gotReason != reason {
		t.Fatalf("reason=%v, want %q", gotReason, reason)
	}
}
