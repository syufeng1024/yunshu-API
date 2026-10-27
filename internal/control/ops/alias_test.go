package ops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	catalog "yunshu.dev/core/internal/control/model"
	"yunshu.dev/core/internal/platform/clock"
)

// F22-2: aliases are added, listed and removed through dedicated endpoints;
// the unique key is case-sensitive (utf8mb4_0900_as_cs).
func TestModelAliasEndpoints(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	m := createTestModel(t, svc, uid, modelBody(t, "t16-alias-model-"+uniqueSuffix(), "CATALOG_ONLY"))

	suffix := uniqueSuffix()
	upper := "Qwen-Max-" + suffix
	lower := "qwen-max-" + suffix
	opts := testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}}

	rec := call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": upper}, opts)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create alias status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": lower}, opts)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create case-variant alias status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}

	rec = call(t, svc.ListAliases, http.MethodGet, "/api/admin/models/1/aliases", nil, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("list aliases status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list aliasListResponse
	decodeBody(t, rec, &list)
	got := map[string]bool{}
	for _, a := range list.Aliases {
		got[a.AliasName] = true
	}
	if !got[upper] || !got[lower] {
		t.Fatalf("aliases=%v, want both case variants kept", list.Aliases)
	}

	opts.pathVal["alias"] = upper
	rec = call(t, svc.DeleteAlias, http.MethodDelete, "/api/admin/models/1/aliases/"+upper, nil, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete alias status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.ListAliases, http.MethodGet, "/api/admin/models/1/aliases", nil,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	decodeBody(t, rec, &list)
	if len(list.Aliases) != 1 || list.Aliases[0].AliasName != lower {
		t.Fatalf("aliases after delete=%v, want only %s", list.Aliases, lower)
	}

	rec = call(t, svc.DeleteAlias, http.MethodDelete, "/api/admin/models/1/aliases/"+upper, nil, opts)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing alias status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

// F22-2: a conflict names the model that already owns the alias, and an alias
// may not take another model's public name.
func TestModelAliasConflictNamesOwner(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	a := createTestModel(t, svc, uid, modelBody(t, "t16-alias-a-"+uniqueSuffix(), "CATALOG_ONLY"))
	b := createTestModel(t, svc, uid, modelBody(t, "t16-alias-b-"+uniqueSuffix(), "CATALOG_ONLY"))

	// Alias equal to A's public name, on B.
	rec := call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": a.ModelName},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(b.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("alias/model-name clash status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	eb := errorBody(t, rec)
	if eb.Error.Code != "resource_state_conflict" ||
		!strings.Contains(eb.Error.Message, a.ModelName) ||
		!strings.Contains(eb.Error.Message, itoa(a.ID)) {
		t.Fatalf("body=%+v, want conflict naming %s (id=%d)", eb.Error, a.ModelName, a.ID)
	}
	t.Logf("A14 alias equal to another model name -> 409 %s: %s", eb.Error.Code, eb.Error.Message)

	// Same alias on two models: the second add names the first owner.
	shared := "shared-alias-" + uniqueSuffix()
	rec = call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": shared},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(a.ID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first alias status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": shared},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(b.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate alias status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	eb = errorBody(t, rec)
	if !strings.Contains(eb.Error.Message, a.ModelName) ||
		!strings.Contains(eb.Error.Message, itoa(a.ID)) {
		t.Fatalf("body=%+v, want the conflicting model id and name", eb.Error)
	}
	t.Logf("A14 duplicate alias %q -> 409 %s: %s", shared, eb.Error.Code, eb.Error.Message)
}

// F22-1: t_model.model_name is the only source; the pricing table and the
// console catalogue read the renamed row without a second copy to update.
func TestModelRenameSyncsPricingAndCatalog(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	oldName := "t16-rename-old-" + uniqueSuffix()
	body := modelBody(t, oldName, "CATALOG_ONLY")
	m := createTestModel(t, svc, uid, body)
	createTestPrice(t, svc, uid, m.ID, 1000, 1000, 600)

	// Alias also reads the model row by id; it must survive the rename.
	rec := call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": "old-alias-" + uniqueSuffix()},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create alias status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = call(t, svc.SetListingState, http.MethodPost, "/api/admin/models/1/listing-state",
		map[string]any{"listing_state": "LISTED"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list model status=%d body=%s", rec.Code, rec.Body.String())
	}

	newName := "t16-rename-new-" + uniqueSuffix()
	rec = call(t, svc.UpdateModel, http.MethodPatch, "/api/admin/models/1",
		map[string]any{"model_name": newName},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(m.ID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	var renamed modelDetailResponse
	decodeBody(t, rec, &renamed)
	if renamed.Model.ModelName != newName {
		t.Fatalf("model_name=%s, want %s", renamed.Model.ModelName, newName)
	}

	// Pricing table: the join reads t_model.model_name.
	rec = call(t, svc.ListPrices, http.MethodGet, "/api/admin/prices?model_id="+itoa(m.ID), nil,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("price list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var prices priceListResponse
	decodeBody(t, rec, &prices)
	foundPrice := false
	for _, p := range prices.Items {
		if p.ModelID == m.ID {
			foundPrice = true
			if p.ModelName != newName {
				t.Fatalf("pricing model_name=%s, want %s", p.ModelName, newName)
			}
		}
	}
	if !foundPrice {
		t.Fatalf("model %d missing from the pricing table", m.ID)
	}

	// Console catalogue: same source, no cached copy.
	catalogSvc := catalog.New(testDB, clock.Real{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/console/models?page_size=100", nil)
	catRec := httptest.NewRecorder()
	catalogSvc.List(catRec, req)
	if catRec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catRec.Code, catRec.Body.String())
	}
	var catalog struct {
		Items []struct {
			ID        int64  `json:"id"`
			ModelName string `json:"model_name"`
		} `json:"items"`
	}
	decodeBody(t, catRec, &catalog)
	foundCatalog := false
	for _, item := range catalog.Items {
		if item.ID == m.ID {
			foundCatalog = true
			if item.ModelName != newName {
				t.Fatalf("catalog model_name=%s, want %s", item.ModelName, newName)
			}
		}
	}
	if !foundCatalog {
		t.Fatalf("model %d missing from the console catalogue", m.ID)
	}
	t.Logf("A13 rename %q -> %q: /api/admin/prices model_name=%q; /api/console/models model_name=%q; alias rows keep model_id=%d",
		oldName, newName, newName, newName, m.ID)
}

// A rename may not collide with another model's public name or alias.
func TestModelRenameRefusesNameAndAliasCollisions(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	a := createTestModel(t, svc, uid, modelBody(t, "t16-rn-a-"+uniqueSuffix(), "CATALOG_ONLY"))
	b := createTestModel(t, svc, uid, modelBody(t, "t16-rn-b-"+uniqueSuffix(), "CATALOG_ONLY"))
	alias := "t16-rn-alias-" + uniqueSuffix()
	rec := call(t, svc.CreateAlias, http.MethodPost, "/api/admin/models/1/aliases",
		map[string]any{"alias_name": alias},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(a.ID)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("alias setup status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = call(t, svc.UpdateModel, http.MethodPatch, "/api/admin/models/1",
		map[string]any{"model_name": a.ModelName},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(b.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename onto model name status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, a.ModelName) ||
		!strings.Contains(msg, itoa(a.ID)) {
		t.Fatalf("message=%q, want the conflicting model", msg)
	}

	rec = call(t, svc.UpdateModel, http.MethodPatch, "/api/admin/models/1",
		map[string]any{"model_name": alias},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(b.ID)}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename onto alias status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, a.ModelName) ||
		!strings.Contains(msg, itoa(a.ID)) {
		t.Fatalf("message=%q, want the alias owner", msg)
	}
}
