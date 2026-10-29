package gw

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yunshu.dev/core/internal/config"
)

// routingMembers is a minimal ROUTING payload that serves one model through
// one channel for key 10 in the standard group.
func routingMembers() map[string]any {
	return map[string]any{
		"models": []map[string]any{{
			"id": 1, "model_name": "cv-model", "region": "DOMESTIC",
			"model_type": "TEXT", "listing_state": "LISTED", "context_length": 4096,
			"max_output_tokens": 16, "default_price_rule_id": 11,
		}},
		"routes": []map[string]any{{
			"id": 1, "model_id": 1, "channel_id": 1, "protocol_id": 1,
			"upstream_model_name": "cv-model", "region": "DOMESTIC",
			"weight": 100, "priority": 100, "stream_supported": true, "enabled": true,
		}},
		"channels": []map[string]any{{
			"id": 1, "name": "cv-channel", "protocol_id": 1, "region": "DOMESTIC",
			"weight": 100, "priority": 100, "group_scope_mode": "ALL",
			"enabled": true, "stream_supported": true,
		}},
		"groups": map[string]any{
			"standard": map[string]any{"code": "standard", "ratio_bp": 10000,
				"model_scope_mode": "ALL", "channel_scope_mode": "ALL", "enabled": true},
		},
		"keyModels": map[string]any{"10": map[string]any{"mode": "ALL"}},
	}
}

// stampedVersion publishes a ROUTING snapshot through the real loader and
// returns a catalog over it plus the version the store reports. A zero version
// would make the assertions below vacuous.
func stampedVersion(t *testing.T) (*Catalog, int64) {
	t.Helper()
	store := writeRouting(t, routingMembers())
	want := store.Snapshot(config.NsRouting).Version()
	if want <= 0 {
		t.Fatalf("routing snapshot version = %d, want > 0", want)
	}
	catalog := NewCatalog(store)
	decoded, err := catalog.Current()
	if err != nil {
		t.Fatalf("catalog.Current: %v", err)
	}
	if decoded.Version != want {
		t.Fatalf("decoded snapshot Version = %d, want %d", decoded.Version, want)
	}
	return catalog, want
}

// TestHandlerStampsRoutingConfigVersion: the public entry must stamp the
// ROUTING snapshot's cfg:ver on the Ctx before P14. Without it the dispatcher
// writes config_version = 0 and R09 never sees a live row.
func TestHandlerStampsRoutingConfigVersion(t *testing.T) {
	catalog, want := stampedVersion(t)
	key := "sk-cfgver"
	var got int64
	handler := NewHandler(serverDeps(catalog, authorizedAuth(key)),
		TerminalFunc(func(w http.ResponseWriter, _ *http.Request, c *Ctx) {
			got = c.ConfigVersion
			w.WriteHeader(http.StatusOK)
		}))

	if rec, _ := postChat(t, handler, key, "cv-model", "rid-cfgver-public"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got <= 0 {
		t.Fatalf("Ctx.ConfigVersion = %d, want > 0", got)
	}
	if got != want {
		t.Fatalf("Ctx.ConfigVersion = %d, want %d", got, want)
	}
}

// TestInternalHandlerStampsRoutingConfigVersion: the T27 debug console builds
// its own Ctx, so it must stamp the version too — otherwise that path keeps
// writing 0 even though the public one is fixed.
func TestInternalHandlerStampsRoutingConfigVersion(t *testing.T) {
	catalog, want := stampedVersion(t)
	var got int64
	terminal := TerminalFunc(func(w http.ResponseWriter, _ *http.Request, c *Ctx) {
		got = c.ConfigVersion
		w.WriteHeader(http.StatusOK)
	})
	handler, _ := newInternalHandler(t, catalog, terminal)

	body := []byte(`{"model":"cv-model","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
	req.Header.Set("X-Request-Id", "rid-cfgver-internal")
	if err := SetInternalHeaders(req.Header, internalTestSecret, time.Now().UTC().Unix(), body, internalTestClaims()); err != nil {
		t.Fatalf("SetInternalHeaders: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got <= 0 {
		t.Fatalf("Ctx.ConfigVersion = %d, want > 0", got)
	}
	if got != want {
		t.Fatalf("Ctx.ConfigVersion = %d, want %d", got, want)
	}
}
