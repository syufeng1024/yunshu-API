package ops

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// withinMillisecond compares two instants stored in a datetime(3) column,
// which rounds rather than truncates.
func withinMillisecond(got, want time.Time) bool {
	diff := got.Sub(want)
	return diff >= -time.Millisecond && diff <= time.Millisecond
}

func createAnnouncementForTest(t *testing.T, svc *Service, uid int64, body map[string]any) announcementView {
	t.Helper()
	rec := call(t, svc.CreateAnnouncement, http.MethodPost, "/api/admin/announcements", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create announcement status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp announcementDetailResponse
	decodeBody(t, rec, &resp)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx,
			`DELETE FROM t_announcement_model WHERE announcement_id = ?`, resp.Announcement.ID)
		_, _ = testDB.ExecContext(ctx,
			`DELETE FROM t_announcement WHERE id = ?`, resp.Announcement.ID)
	})
	return resp.Announcement
}

// A10: effective_from is writable and bounded below by publish_at; offline is
// one-way and a second call is a 409.
func TestAnnouncementEffectiveFromAndOffline(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")

	now := time.Now().UTC()
	publishAt := now.Add(24 * time.Hour)
	body := newAnnouncementBody("T24 生效时间", "NOTICE", "t24")
	body["publish_at"] = publishAt
	body["effective_from"] = publishAt.Add(-time.Hour)
	rec := call(t, svc.CreateAnnouncement, http.MethodPost, "/api/admin/announcements", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("effective_from before publish_at status=%d body=%s, want 400",
			rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "effective_from") {
		t.Fatalf("refusal message=%q, want the field named", msg)
	}

	body["effective_from"] = publishAt.Add(time.Hour)
	created := createAnnouncementForTest(t, svc, uid, body)
	if created.State != annScheduled {
		t.Fatalf("state=%s, want SCHEDULED", created.State)
	}
	// t_announcement.effective_from is datetime(3); MySQL rounds to the nearest
	// millisecond, so allow a 1ms difference instead of comparing nanoseconds.
	if !withinMillisecond(created.EffectiveFrom, publishAt.Add(time.Hour)) {
		t.Fatalf("effective_from=%s, want %s", created.EffectiveFrom, publishAt.Add(time.Hour))
	}

	rec = call(t, svc.OfflineAnnouncement, http.MethodPost,
		fmt.Sprintf("/api/admin/announcements/%d/offline", created.ID), nil,
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(created.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("offline status=%d body=%s", rec.Code, rec.Body.String())
	}
	var offline announcementDetailResponse
	decodeBody(t, rec, &offline)
	if offline.Announcement.State != annOffline {
		t.Fatalf("state=%s, want OFFLINE", offline.Announcement.State)
	}
	rec = call(t, svc.OfflineAnnouncement, http.MethodPost,
		fmt.Sprintf("/api/admin/announcements/%d/offline", created.ID), nil,
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(created.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second offline status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}

	// A draft accepts effective_from on PATCH, and a published notice refuses
	// the edit.
	draftBody := newAnnouncementBody("T24 草稿生效时间", "NOTICE", "t24")
	draftBody["effective_from"] = now.Add(-time.Hour)
	draft := createAnnouncementForTest(t, svc, uid, draftBody)
	if draft.State != annDraft {
		t.Fatalf("draft state=%s, want DRAFT", draft.State)
	}
	nextFrom := now.Add(2 * time.Hour)
	rec = call(t, svc.UpdateAnnouncement, http.MethodPatch,
		fmt.Sprintf("/api/admin/announcements/%d", draft.ID),
		map[string]any{"effective_from": nextFrom},
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(draft.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch effective_from status=%d body=%s", rec.Code, rec.Body.String())
	}
	var patched announcementDetailResponse
	decodeBody(t, rec, &patched)
	if !withinMillisecond(patched.Announcement.EffectiveFrom, nextFrom) {
		t.Fatalf("patched effective_from=%s, want %s",
			patched.Announcement.EffectiveFrom, nextFrom)
	}

	rec = call(t, svc.PublishAnnouncement, http.MethodPost,
		fmt.Sprintf("/api/admin/announcements/%d/publish", draft.ID), nil,
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(draft.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.UpdateAnnouncement, http.MethodPatch,
		fmt.Sprintf("/api/admin/announcements/%d", draft.ID),
		map[string]any{"effective_from": now.Add(3 * time.Hour)},
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(draft.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("patch published effective_from status=%d body=%s, want 409",
			rec.Code, rec.Body.String())
	}
	t.Logf("A10: effective_from before publish_at → 400; scheduled=%s; offline=%s; second offline=%d; published patch=%d",
		created.State, offline.Announcement.State, http.StatusConflict, rec.Code)
}

// A10 second half: model_ids is persisted and returned by the list endpoint
// with no N+1 query.
func TestAnnouncementModelIDsInList(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	m1 := newPricingModel(t, svc, uid, "TEXT")
	m2 := newPricingModel(t, svc, uid, "TEXT")

	withModels := newAnnouncementBody("T24 关联模型", "PRICE_CHANGE", "t24")
	withModels["model_ids"] = []int64{m2.ID, m1.ID, m1.ID} // duplicate is dropped
	linked := createAnnouncementForTest(t, svc, uid, withModels)
	if len(linked.ModelIDs) != 2 {
		t.Fatalf("create response model_ids=%v, want 2 ids", linked.ModelIDs)
	}

	plain := createAnnouncementForTest(t, svc, uid,
		newAnnouncementBody("T24 无关联模型", "NOTICE", "t24"))
	if plain.ModelIDs == nil || len(plain.ModelIDs) != 0 {
		t.Fatalf("plain model_ids=%v, want []", plain.ModelIDs)
	}

	rec := call(t, svc.ListAnnouncements, http.MethodGet,
		"/api/admin/announcements?page=1&page_size=100", nil,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list announcementListResponse
	decodeBody(t, rec, &list)
	byID := make(map[int64]announcementView, len(list.Items))
	for _, item := range list.Items {
		byID[item.ID] = item
	}
	got := byID[linked.ID].ModelIDs
	if len(got) != 2 || got[0] != m1.ID || got[1] != m2.ID {
		t.Fatalf("list model_ids=%v, want [%d %d]", got, m1.ID, m2.ID)
	}
	if plainItem, ok := byID[plain.ID]; !ok || plainItem.ModelIDs == nil || len(plainItem.ModelIDs) != 0 {
		t.Fatalf("plain list model_ids=%v (present=%v), want []", byID[plain.ID].ModelIDs, ok)
	}

	// A PATCH that does not mention model_ids keeps the associations; a PATCH
	// with [] clears them.
	rec = call(t, svc.UpdateAnnouncement, http.MethodPatch,
		fmt.Sprintf("/api/admin/announcements/%d", linked.ID),
		map[string]any{"title": "T24 改名"},
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(linked.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch title status=%d body=%s", rec.Code, rec.Body.String())
	}
	var renamed announcementDetailResponse
	decodeBody(t, rec, &renamed)
	if len(renamed.Announcement.ModelIDs) != 2 {
		t.Fatalf("model_ids after title-only patch=%v, want unchanged", renamed.Announcement.ModelIDs)
	}
	rec = call(t, svc.UpdateAnnouncement, http.MethodPatch,
		fmt.Sprintf("/api/admin/announcements/%d", linked.ID),
		map[string]any{"model_ids": []int64{}},
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(linked.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear model_ids status=%d body=%s", rec.Code, rec.Body.String())
	}
	var cleared announcementDetailResponse
	decodeBody(t, rec, &cleared)
	if cleared.Announcement.ModelIDs == nil || len(cleared.Announcement.ModelIDs) != 0 {
		t.Fatalf("model_ids after [] patch=%v, want []", cleared.Announcement.ModelIDs)
	}
	var rowsAfterClear int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_announcement_model WHERE announcement_id = ?`,
		linked.ID).Scan(&rowsAfterClear); err != nil {
		t.Fatal(err)
	}
	if rowsAfterClear != 0 {
		t.Fatalf("t_announcement_model rows=%d after clear, want 0", rowsAfterClear)
	}
	t.Logf("model_ids: linked=%v plain=%v; title-only patch keeps associations; [] clears them",
		got, byID[plain.ID].ModelIDs)
}

func nullableInt(t *testing.T, priceID int64, column string) *int64 {
	t.Helper()
	var v sql.NullInt64
	q := `SELECT ` + column + ` FROM t_price_rule WHERE id = ?`
	if err := testDB.QueryRowContext(context.Background(), q, priceID).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", column, err)
	}
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

// A11: clear_fields is whitelisted, refuses assignment+clear on the same field,
// and actually writes NULL.
func TestPriceClearFieldsWhitelist(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	create := priceBody(model.ID, 1000, 3000, 600)
	create["cache_hit_ratio_bp"] = 8000
	create["cache_write_ratio_bp"] = 12000
	create["effective_to"] = time.Now().UTC().Add(30 * 24 * time.Hour)
	rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices", create,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create price status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created priceDetailResponse
	decodeBody(t, rec, &created)
	priceID := created.Price.ID
	path := fmt.Sprintf("/api/admin/prices/%d", priceID)
	opts := testCallOptions{uid: uid, role: "OPS",
		pathVal: map[string]string{"id": fmt.Sprint(priceID)}}

	rec = call(t, svc.UpdatePrice, http.MethodPatch, path,
		map[string]any{"clear_fields": []string{"cache_hit_ratio_bp"}}, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear cache_hit status=%d body=%s", rec.Code, rec.Body.String())
	}
	if v := nullableInt(t, priceID, "cache_hit_ratio_bp"); v != nil {
		t.Fatalf("cache_hit_ratio_bp=%d after clear, want NULL", *v)
	}

	rec = call(t, svc.UpdatePrice, http.MethodPatch, path,
		map[string]any{"clear_fields": []string{"not_a_field"}}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown clear field status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "clear_fields") {
		t.Fatalf("unknown clear field message=%q", msg)
	}

	rec = call(t, svc.UpdatePrice, http.MethodPatch, path,
		map[string]any{
			"cache_write_ratio_bp": 5000,
			"clear_fields":         []string{"cache_write_ratio_bp"},
		}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("assign+clear status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "cache_write_ratio_bp") {
		t.Fatalf("assign+clear message=%q, want the field named", msg)
	}

	rec = call(t, svc.UpdatePrice, http.MethodPatch, path,
		map[string]any{"clear_fields": []string{"effective_to"}}, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear effective_to status=%d body=%s", rec.Code, rec.Body.String())
	}
	var effectiveTo sql.NullTime
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT effective_to FROM t_price_rule WHERE id = ?`, priceID).Scan(&effectiveTo); err != nil {
		t.Fatal(err)
	}
	if effectiveTo.Valid {
		t.Fatalf("effective_to=%s after clear, want NULL", effectiveTo.Time)
	}

	// Clearing cost fields makes the row costless; the existing confirm gate
	// still applies, and an explicit confirmation is recorded in the audit.
	rec = call(t, svc.UpdatePrice, http.MethodPatch, path,
		map[string]any{"clear_fields": []string{"cost_in_micro_per_k", "cost_out_micro_per_k"}}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("clear costs without confirm status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.UpdatePrice, http.MethodPatch, path,
		map[string]any{
			"clear_fields":    []string{"cost_in_micro_per_k", "cost_out_micro_per_k"},
			"confirm_no_cost": true,
		}, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear costs with confirm status=%d body=%s", rec.Code, rec.Body.String())
	}
	if v := nullableInt(t, priceID, "cost_in_micro_per_k"); v != nil {
		t.Fatalf("cost_in=%d after clear, want NULL", *v)
	}
	t.Logf("A11: clear_fields whitelist enforced; cache_hit/effective_to/cost cleared to NULL; assign+clear → 400")
}

// A12: recommend_weight is capped at 1000 and the refusal names the field.
func TestRecommendWeightUpperBound(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")
	path := fmt.Sprintf("/api/admin/models/%d/recommend-weight", model.ID)
	opts := testCallOptions{uid: uid, role: "OPS",
		pathVal: map[string]string{"id": fmt.Sprint(model.ID)}}

	rec := call(t, svc.SetRecommendWeight, http.MethodPost, path,
		map[string]any{"recommend_weight": 1001}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weight 1001 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "recommend_weight") {
		t.Fatalf("weight refusal message=%q, want the field named", msg)
	}

	createTestPrice(t, svc, uid, model.ID, 1000, 3000, 600)
	rec = call(t, svc.SetListingState, http.MethodPost,
		fmt.Sprintf("/api/admin/models/%d/listing-state", model.ID),
		map[string]any{"listing_state": "LISTED", "reason": "测试上架"},
		opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing state status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = call(t, svc.SetRecommendWeight, http.MethodPost, path,
		map[string]any{"recommend_weight": 1000}, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("weight 1000 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var weight int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT recommend_weight FROM t_model WHERE id = ?`, model.ID).Scan(&weight); err != nil {
		t.Fatal(err)
	}
	if weight != 1000 {
		t.Fatalf("recommend_weight=%d, want 1000", weight)
	}
	rec = call(t, svc.SetRecommendWeight, http.MethodPost, path,
		map[string]any{"recommend_weight": 1001}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("listed weight 1001 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	// The create path applies the same cap before the listing-state refusal, so
	// 1001 is always a body error.
	rec = call(t, svc.CreateModel, http.MethodPost, "/api/admin/models",
		map[string]any{
			"model_name":       "t24-cap-" + uniqueSuffix(),
			"display_name":     "T24 上限模型",
			"region":           "DOMESTIC",
			"model_type":       "TEXT",
			"protocol_id":      protocolID(t, "OPENAI_COMPAT"),
			"listing_state":    "CATALOG_ONLY",
			"recommend_weight": 1001,
		}, testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create weight 1001 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	t.Logf("A12: 1001 → 400 naming recommend_weight; 1000 accepted for LISTED model")
}
