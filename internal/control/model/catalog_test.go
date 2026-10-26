package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

var seq uint64

func newCatalogModel(t *testing.T, name, modelType, state, protocol string, weight int64, published time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_model
			(model_name, display_name, region, model_type, protocol_id, context_length,
			 capability_mask, listing_state, recommend_weight, published_at, created_at, updated_at)
		SELECT ?, ?, 'DOMESTIC', ?, p.id, 32768, 1, ?, ?, ?, NOW(3), NOW(3)
		  FROM t_protocol p WHERE p.code = ?`,
		name, name, modelType, state, weight, published, protocol)
	if err != nil {
		t.Fatalf("insert model: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("model id: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_price_rule WHERE scope_level = 'MODEL' AND scope_id = ?`, id)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_model_alias WHERE model_id = ?`, id)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_model_capability WHERE model_id = ?`, id)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_model WHERE id = ?`, id)
	})
	return id
}

func newPrice(t *testing.T, modelID, inMicro, outMicro int64, cacheHit *int64) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_price_rule
			(scope_level, scope_id, model_type, in_price_micro_per_k, out_price_micro_per_k,
			 per_call_price_micro, cache_hit_ratio_bp, effective_from, effective_to,
			 version, enabled, created_by, created_at)
		VALUES ('MODEL', ?, 'TEXT', ?, ?, 0, ?, '2026-01-01 00:00:00.000', NULL, 1, 1, 0, NOW(3))`,
		modelID, inMicro, outMicro, cacheHit); err != nil {
		t.Fatalf("insert price: %v", err)
	}
}

func newAlias(t *testing.T, modelID int64, alias string) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_model_alias (alias_name, model_id, source, enabled, created_at)
		VALUES (?,?,'OPS',1,NOW(3))`, alias, modelID); err != nil {
		t.Fatalf("insert alias: %v", err)
	}
}

func catalogRequest(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	svc := New(testDB, clock.Real{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/console/models"+query, nil)
	rec := httptest.NewRecorder()
	svc.List(rec, req)
	return rec
}

func decodeCatalog(t *testing.T, rec *httptest.ResponseRecorder) catalogResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp catalogResponse
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return resp
}

func indexOf(items []catalogItem, id int64) int {
	for i, item := range items {
		if item.ID == id {
			return i
		}
	}
	return -1
}

// A8 / F8-4: default order is (recommend_weight DESC, published_at DESC), and
// a type filter keeps that order instead of falling back to insertion order.
func TestCatalogOrderingSurvivesFilter(t *testing.T) {
	prefix := fmt.Sprintf("t12cat-%d", time.Now().UnixNano())
	m1 := newCatalogModel(t, prefix+"-M1", "TEXT", "LISTED", "OPENAI_COMPAT", 100, time.Now().Add(-48*time.Hour))
	m2 := newCatalogModel(t, prefix+"-M2", "TEXT", "LISTED", "OPENAI_COMPAT", 0, time.Now())
	newPrice(t, m1, 1500, 3000, nil)
	newPrice(t, m2, 1000, 2000, nil)

	all := decodeCatalog(t, catalogRequest(t, "?q="+prefix))
	if len(all.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(all.Items))
	}
	if indexOf(all.Items, m1) > indexOf(all.Items, m2) {
		t.Fatalf("M1 (weight 100) is not before M2 (weight 0): %+v", all.Items)
	}

	filtered := decodeCatalog(t, catalogRequest(t, "?model_type=TEXT&q="+prefix))
	if len(filtered.Items) != 2 {
		t.Fatalf("filtered items = %d, want 2", len(filtered.Items))
	}
	if indexOf(filtered.Items, m1) > indexOf(filtered.Items, m2) {
		t.Fatalf("type filter changed the order: %+v", filtered.Items)
	}
}

// A9 / F8-3: CATALOG_ONLY never appears, and with cache billing disabled no
// cache price is emitted at all.
func TestCatalogHidesCatalogOnlyAndCachePrice(t *testing.T) {
	prefix := fmt.Sprintf("t12cat-%d", time.Now().UnixNano())
	listed := newCatalogModel(t, prefix+"-LISTED", "TEXT", "LISTED", "OPENAI_COMPAT", 10, time.Now())
	catalogOnly := newCatalogModel(t, prefix+"-CATALOG", "TEXT", "CATALOG_ONLY", "OPENAI_COMPAT", 99, time.Now())
	newPrice(t, listed, 1500, 3000, int64Ptr(1000))
	newPrice(t, catalogOnly, 1500, 3000, nil)

	resp := decodeCatalog(t, catalogRequest(t, "?q="+prefix))
	if len(resp.Items) != 1 || resp.Items[0].ID != listed {
		t.Fatalf("items = %+v, want only the LISTED model %d", resp.Items, listed)
	}
	if indexOf(resp.Items, catalogOnly) >= 0 {
		t.Fatal("CATALOG_ONLY model appears in the catalog")
	}
	pricing := resp.Items[0].Pricing
	if pricing == nil {
		t.Fatal("pricing is null")
	}
	if pricing.CacheBillingEnabled {
		t.Fatal("cache billing is enabled unexpectedly")
	}
	if pricing.CacheHitRatioBP != nil || pricing.CacheWriteRatioBP != nil ||
		pricing.CacheHitPriceCredits != nil || pricing.CacheWritePriceCredits != nil {
		t.Fatalf("cache price leaked while billing is off: %+v", pricing)
	}

	// Prove the wire form omits the keys, not merely sets them to zero.
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := jsonx.Unmarshal(catalogRequest(t, "?q="+prefix).Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	pricingRaw, ok := raw.Items[0]["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("pricing is not an object: %v", raw.Items[0]["pricing"])
	}
	for _, key := range []string{"cache_hit_ratio_bp", "cache_write_ratio_bp",
		"cache_hit_price_credits_per_k", "cache_write_price_credits_per_k"} {
		if _, exists := pricingRaw[key]; exists {
			t.Fatalf("wire pricing contains %q with cache billing disabled", key)
		}
	}
}

// A10 / F8-2: 1500 micro-credits per 1k tokens is 0.15 credits and 0.0015 CNY.
func TestCatalogPriceConversion(t *testing.T) {
	prefix := fmt.Sprintf("t12cat-%d", time.Now().UnixNano())
	id := newCatalogModel(t, prefix+"-PRICE", "TEXT", "LISTED", "OPENAI_COMPAT", 1, time.Now())
	newPrice(t, id, 1500, 3000, nil)

	resp := decodeCatalog(t, catalogRequest(t, "?q="+prefix))
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d", len(resp.Items))
	}
	p := resp.Items[0].Pricing
	if p == nil {
		t.Fatal("pricing is null")
	}
	if p.InPriceMicroPerK != 1500 {
		t.Fatalf("in_price_micro_per_k = %d", p.InPriceMicroPerK)
	}
	if p.InPriceCreditsPerK != "0.15" {
		t.Fatalf("in_price_credits_per_k = %q, want 0.15", p.InPriceCreditsPerK)
	}
	if p.InPriceCNYPerK != "0.0015" {
		t.Fatalf("in_price_cny_per_k = %q, want 0.0015", p.InPriceCNYPerK)
	}
	// The standard price must not be multiplied by any group ratio.
	if p.OutPriceCreditsPerK != "0.3" || p.OutPriceCNYPerK != "0.003" {
		t.Fatalf("out price = %q/%q", p.OutPriceCreditsPerK, p.OutPriceCNYPerK)
	}
}

// F8-1: the catalog reflects the stored context length, not a hard-coded value.
func TestCatalogUsesStoredContextLength(t *testing.T) {
	prefix := fmt.Sprintf("t12cat-%d", time.Now().UnixNano())
	id := newCatalogModel(t, prefix+"-CTX", "TEXT", "LISTED", "OPENAI_COMPAT", 1, time.Now())
	newPrice(t, id, 1000, 2000, nil)
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_model SET context_length = 123456 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	resp := decodeCatalog(t, catalogRequest(t, "?q="+prefix))
	if len(resp.Items) != 1 || resp.Items[0].ContextLength != 123456 {
		t.Fatalf("context_length = %v", resp.Items)
	}
}

func TestCatalogAliasAndProtocolFilters(t *testing.T) {
	prefix := fmt.Sprintf("t12cat-%d", time.Now().UnixNano())
	openai := newCatalogModel(t, prefix+"-OAI", "TEXT", "LISTED", "OPENAI_COMPAT", 1, time.Now())
	bailian := newCatalogModel(t, prefix+"-BL", "TEXT", "LISTED", "BAILIAN", 2, time.Now())
	newPrice(t, openai, 1000, 2000, nil)
	newPrice(t, bailian, 1000, 2000, nil)
	alias := fmt.Sprintf("%s-alias", prefix)
	newAlias(t, openai, alias)

	byAlias := decodeCatalog(t, catalogRequest(t, "?q="+alias))
	if indexOf(byAlias.Items, openai) < 0 {
		t.Fatalf("alias search missed the model: %+v", byAlias.Items)
	}
	byProto := decodeCatalog(t, catalogRequest(t, "?protocol=BAILIAN&q="+prefix))
	if indexOf(byProto.Items, bailian) < 0 {
		t.Fatal("protocol filter missed BAILIAN")
	}
	if indexOf(byProto.Items, openai) >= 0 {
		t.Fatal("protocol filter returned an OPENAI_COMPAT model")
	}
}

// A9 companion: a model without a price is LISTED but has null pricing rather
// than a fake zero.
func TestCatalogMissingPriceIsNull(t *testing.T) {
	prefix := fmt.Sprintf("t12cat-%d", time.Now().UnixNano())
	id := newCatalogModel(t, prefix+"-NOPRICE", "TEXT", "LISTED", "OPENAI_COMPAT", 1, time.Now())
	resp := decodeCatalog(t, catalogRequest(t, "?q="+prefix))
	if len(resp.Items) != 1 || resp.Items[0].ID != id {
		t.Fatalf("items = %+v", resp.Items)
	}
	if resp.Items[0].Pricing != nil {
		t.Fatalf("pricing = %+v, want null", resp.Items[0].Pricing)
	}
}

func int64Ptr(v int64) *int64 { return &v }
