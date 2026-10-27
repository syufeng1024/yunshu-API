package ops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A16 / F22-4: region / type / listing-state filters, and listing_state is
// present on every row so the console can colour CATALOG_ONLY differently.
func TestModelListFiltersAndReturnsListingState(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")

	textCatalog := createTestModel(t, svc, uid,
		modelBody(t, "t16-filter-text-cat-"+uniqueSuffix(), "CATALOG_ONLY"))
	imageCatalogBody := modelBody(t, "t16-filter-image-cat-"+uniqueSuffix(), "CATALOG_ONLY")
	imageCatalogBody["model_type"] = "IMAGE"
	imageCatalog := createTestModel(t, svc, uid, imageCatalogBody)

	textListed := createTestModel(t, svc, uid,
		modelBody(t, "t16-filter-text-listed-"+uniqueSuffix(), "CATALOG_ONLY"))
	createTestPrice(t, svc, uid, textListed.ID, 1000, 1000, 600)
	rec := call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "LISTED", "reason": "测试上架"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(textListed.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list model status=%d body=%s", rec.Code, rec.Body.String())
	}

	type check struct {
		query string
		want  map[int64]bool
		not   map[int64]bool
	}
	checks := []check{
		{
			query: "?model_type=TEXT&page_size=100",
			want:  map[int64]bool{textCatalog.ID: true, textListed.ID: true},
			not:   map[int64]bool{imageCatalog.ID: true},
		},
		{
			query: "?model_type=IMAGE&page_size=100",
			want:  map[int64]bool{imageCatalog.ID: true},
			not:   map[int64]bool{textCatalog.ID: true, textListed.ID: true},
		},
		{
			query: "?listing_state=CATALOG_ONLY&page_size=100",
			want:  map[int64]bool{textCatalog.ID: true, imageCatalog.ID: true},
			not:   map[int64]bool{textListed.ID: true},
		},
		{
			query: "?listing_state=LISTED&page_size=100",
			want:  map[int64]bool{textListed.ID: true},
			not:   map[int64]bool{textCatalog.ID: true, imageCatalog.ID: true},
		},
		{
			query: "?region=DOMESTIC&page_size=100",
			want:  map[int64]bool{textCatalog.ID: true, imageCatalog.ID: true, textListed.ID: true},
		},
		{
			query: "?region=OVERSEAS&page_size=100",
			not:   map[int64]bool{textCatalog.ID: true, imageCatalog.ID: true, textListed.ID: true},
		},
	}
	for _, tc := range checks {
		t.Run(tc.query, func(t *testing.T) {
			rec := call(t, svc.ListModels, http.MethodGet, "/api/admin/models"+tc.query, nil,
				testCallOptions{uid: uid, role: "OPS"})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var resp modelListResponse
			decodeBody(t, rec, &resp)
			seen := make(map[int64]bool, len(resp.Items))
			for _, item := range resp.Items {
				seen[item.ID] = true
				if item.ListingState != listingListed && item.ListingState != listingCatalogOnly &&
					item.ListingState != listingDelisted {
					t.Fatalf("item %d listing_state=%q, want a concrete state", item.ID, item.ListingState)
				}
			}
			for id := range tc.want {
				if !seen[id] {
					t.Fatalf("model %d missing from %s", id, tc.query)
				}
			}
			for id := range tc.not {
				if seen[id] {
					t.Fatalf("model %d leaked into %s", id, tc.query)
				}
			}
			t.Logf("A16 %s -> total=%d; text_catalog=%v image_catalog=%v text_listed=%v",
				tc.query, resp.Total, seen[textCatalog.ID], seen[imageCatalog.ID], seen[textListed.ID])
		})
	}
}

// F22-3: a listing-state draft records the operator and the reason. The draft
// row has no reason column, so the reason lives in the ANNOUNCEMENT audit row
// next to created_by.
func TestModelListingDraftRecordsOperatorAndReason(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	m := createTestModel(t, svc, uid,
		modelBody(t, "t16-draft-reason-"+uniqueSuffix(), "CATALOG_ONLY"))
	createTestPrice(t, svc, uid, m.ID, 1000, 1000, 600)
	rec := call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "LISTED", "reason": "首次上架"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}

	reason := "合规调整：上游条款变更"
	rec = call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "CATALOG_ONLY", "reason": reason},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("unlist status=%d body=%s", rec.Code, rec.Body.String())
	}

	var (
		announcementID string
		auditReason    string
		after          []byte
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT target_id, reason, after_json FROM t_admin_audit_log
		 WHERE module = 'ANNOUNCEMENT' AND action = 'CREATE' AND operator_id = ?
		 ORDER BY id DESC LIMIT 1`, uid).Scan(&announcementID, &auditReason, &after); err != nil {
		t.Fatalf("read announcement audit: %v", err)
	}
	if auditReason != reason {
		t.Fatalf("audit reason=%q, want %q", auditReason, reason)
	}
	if !strings.Contains(string(after), fmt.Sprintf(`"operator_id": %d`, uid)) ||
		!strings.Contains(string(after), `"listing_state": "CATALOG_ONLY"`) {
		t.Fatalf("after_json=%s, want operator_id and listing_state", after)
	}

	var (
		createdBy int64
		state     string
	)
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT created_by, state FROM t_announcement WHERE id = ?`, announcementID).
		Scan(&createdBy, &state); err != nil {
		t.Fatalf("read draft: %v", err)
	}
	if createdBy != uid || state != "DRAFT" {
		t.Fatalf("draft created_by=%d state=%s, want %d/DRAFT", createdBy, state, uid)
	}
	t.Logf("F22-3 draft announcement=%s created_by=%d state=%s; audit reason=%q after_json=%s",
		announcementID, createdBy, state, auditReason, after)
}
