package ops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

func newAnnouncementBody(title, annType, modelName string) map[string]any {
	return map[string]any{
		"title":         title,
		"content":       "T15 公告 " + modelName,
		"ann_type":      annType,
		"position_mask": 2,
	}
}

// A14: a scheduled notice publishes when the injected clock reaches
// effective_from. No sleep, no wall-clock dependency.
func TestScheduledAnnouncementPublishesAtDueTime(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	base := time.Now().UTC()
	svc.Now = clock.NewFixed(base)

	body := newAnnouncementBody("定时公告", "NOTICE", model.ModelName)
	body["publish_at"] = base.Add(time.Hour)
	rec := call(t, svc.CreateAnnouncement, http.MethodPost, "/api/admin/announcements", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scheduled status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created announcementDetailResponse
	decodeBody(t, rec, &created)
	if created.Announcement.State != annScheduled {
		t.Fatalf("state = %s, want SCHEDULED", created.Announcement.State)
	}

	// Before the due time the scheduler must leave it alone.
	n, err := svc.PublishDueAnnouncements(context.Background())
	if err != nil {
		t.Fatalf("scheduler early: %v", err)
	}
	if n != 0 {
		t.Fatalf("scheduler published %d early rows, want 0", n)
	}

	svc.Now = clock.NewFixed(base.Add(2 * time.Hour))
	n, err = svc.PublishDueAnnouncements(context.Background())
	if err != nil {
		t.Fatalf("scheduler due: %v", err)
	}
	if n != 1 {
		t.Fatalf("scheduler published %d rows, want 1", n)
	}
	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_announcement WHERE id = ?`, created.Announcement.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != annPublished {
		t.Fatalf("state = %s, want PUBLISHED", state)
	}

	// Hard constraint 12: publishing a notice must not rewind the model's
	// listing state. The notice is a notification, not a state-machine driver.
	var listing string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT listing_state FROM t_model WHERE id = ?`, model.ID).Scan(&listing); err != nil {
		t.Fatal(err)
	}
	if listing != model.ListingState {
		t.Fatalf("listing_state = %s, want unchanged %s", listing, model.ListingState)
	}
	t.Logf("A14 clock %s → %s; model listing_state=%s unchanged",
		base.Add(2*time.Hour).Format(time.RFC3339), state, listing)
}

// F16-4: an activity notice may not be published while a price increase is
// inside its public-notice window.
func TestPromotionAnnouncementConflictsWithPriceNotice(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	rec := call(t, svc.CreatePrice, http.MethodPost, "/api/admin/prices",
		priceBody(model.ID, 1000, 1000, 600), testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("initial price status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Plant a pending increase 3 days out: the write path would normally have
	// refused it, but an imported/legacy row must still block a promotion.
	insertRawPriceRule(t, model.ID, 1200, time.Now().UTC().Add(3*24*time.Hour))

	body := newAnnouncementBody("限时活动", "PROMOTION", model.ModelName)
	rec = call(t, svc.CreateAnnouncement, http.MethodPost, "/api/admin/announcements", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("promotion status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	msg := errorBody(t, rec).Error.Message
	if !strings.Contains(msg, "涨价") || !strings.Contains(msg, model.ModelName) {
		t.Fatalf("conflict message = %q, want the model and the price increase", msg)
	}
	t.Logf("A13 promotion conflict → HTTP 400: %s", msg)

	// A PRICE_CHANGE notice is the allowed channel for the same event.
	body = newAnnouncementBody("调价公示", "PRICE_CHANGE", model.ModelName)
	rec = call(t, svc.CreateAnnouncement, http.MethodPost, "/api/admin/announcements", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("price change status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
}

// Publish is idempotence-guarded and does not touch t_model.
func TestPublishAnnouncementDoesNotRollbackModelState(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	model := newPricingModel(t, svc, uid, "TEXT")

	rec := call(t, svc.CreateAnnouncement, http.MethodPost, "/api/admin/announcements",
		newAnnouncementBody("普通公告", "NOTICE", model.ModelName),
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create draft status=%d body=%s", rec.Code, rec.Body.String())
	}
	var draft announcementDetailResponse
	decodeBody(t, rec, &draft)

	rec = call(t, svc.PublishAnnouncement, http.MethodPost,
		fmt.Sprintf("/api/admin/announcements/%d/publish", draft.Announcement.ID), nil,
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(draft.Announcement.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", rec.Code, rec.Body.String())
	}
	var published announcementDetailResponse
	decodeBody(t, rec, &published)
	if published.Announcement.State != annPublished {
		t.Fatalf("state = %s, want PUBLISHED", published.Announcement.State)
	}

	rec = call(t, svc.PublishAnnouncement, http.MethodPost,
		fmt.Sprintf("/api/admin/announcements/%d/publish", draft.Announcement.ID), nil,
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(draft.Announcement.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("republish status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}

	var listing string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT listing_state FROM t_model WHERE id = ?`, model.ID).Scan(&listing); err != nil {
		t.Fatal(err)
	}
	if listing != model.ListingState {
		t.Fatalf("listing_state = %s, want %s unchanged (announcement must not change it)",
			listing, model.ListingState)
	}
}
