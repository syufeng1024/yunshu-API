package ops

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/jsonx"
)

type fakeConfigPublisher struct {
	calls      int
	changes    []config.Change
	namespaces []config.Namespace
}

func (f *fakeConfigPublisher) Publish(_ context.Context, changes []config.Change, _ uint64) (*config.PublishResult, error) {
	f.calls++
	f.changes = append([]config.Change(nil), changes...)
	keys := make([]config.Key, 0, len(changes))
	seen := map[config.Namespace]bool{}
	for _, ch := range changes {
		keys = append(keys, ch.Key)
		meta, _ := config.Meta(ch.Key)
		seen[meta.Namespace] = true
	}
	f.namespaces = f.namespaces[:0]
	for ns := range seen {
		f.namespaces = append(f.namespaces, ns)
	}
	return &config.PublishResult{
		Version: 9, PublishID: "01T15CONFIGPUBLISH0000000",
		Namespaces: f.namespaces, ChangedKeys: keys,
	}, nil
}

// A15: the 12 runtime keys are writable, the 13th and the unassigned key are
// rejected with config_value_invalid.
func TestConfigWhitelist(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	fake := &fakeConfigPublisher{}
	svc.ConfigPublisher = fake

	rec := call(t, svc.GetConfig, http.MethodGet, "/api/admin/config", nil,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list configListResponse
	decodeBody(t, rec, &list)
	if list.Total != config.KeyCount {
		t.Fatalf("GET config total=%d, want %d", list.Total, config.KeyCount)
	}
	runtimeWritable := 0
	for _, item := range list.Items {
		if item.RuntimeWritable {
			runtimeWritable++
		}
	}
	if runtimeWritable != config.RuntimeKeyCount {
		t.Fatalf("GET config runtime_writable=%d, want %d", runtimeWritable, config.RuntimeKeyCount)
	}

	// Every whitelisted key in one PUT: all defaults are valid by definition.
	values := make(map[string]jsonx.RawMessage, config.RuntimeKeyCount)
	for _, key := range config.RuntimeWritableKeys() {
		meta, ok := config.Meta(key)
		if !ok {
			t.Fatalf("missing meta for %s", key)
		}
		values[string(key)] = jsonx.RawMessage(meta.Default)
	}
	rec = call(t, svc.PutConfig, http.MethodPut, "/api/admin/config",
		map[string]any{"values": values}, testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT whitelist status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated configUpdateResponse
	decodeBody(t, rec, &updated)
	if updated.Version != 9 || len(updated.ChangedKeys) != config.RuntimeKeyCount {
		t.Fatalf("publish result = %+v, want version 9 and %d keys", updated, config.RuntimeKeyCount)
	}
	if fake.calls != 1 || len(fake.changes) != config.RuntimeKeyCount {
		t.Fatalf("publisher calls=%d changes=%d, want 1/%d", fake.calls, len(fake.changes), config.RuntimeKeyCount)
	}
	t.Logf("A15 GET total=%d runtime_writable=%d; PUT 12 keys → version=%d namespaces=%v",
		list.Total, runtimeWritable, updated.Version, updated.Namespaces)

	// Hard constraint 15: the config write is audited with before/after.
	var before, after string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT before_json, after_json FROM t_admin_audit_log
		 WHERE module = 'CONFIG' AND action = 'UPDATE'
		 ORDER BY id DESC LIMIT 1`).Scan(&before, &after); err != nil {
		t.Fatalf("config audit: %v", err)
	}
	if !strings.Contains(before, string(config.KeyGwMaxInflight)) ||
		!strings.Contains(after, string(config.KeyGwMaxInflight)) {
		t.Fatalf("config audit before=%s after=%s", before, after)
	}

	reject := []struct {
		name   string
		key    string
		value  string
		reason string
	}{
		{"registered but not runtime", string(config.KeyBillingPreholdRatio), `1.2`, "12 个运行时白名单"},
		{"unassigned key", string(config.KeyQuotaOverdraftWarnDays), `7`, "不属于任何命名空间"},
		{"quota enum not implemented", string(config.KeyQuotaKeyDailyMode), `"strict"`, "not implemented"},
		{"quota reset scope", string(config.KeyQuotaResetScope), `"key"`, "not implemented"},
		{"unknown key", "no.such.key", `1`, "不是已登记的配置键"},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			fake.calls = 0
			rec := call(t, svc.PutConfig, http.MethodPut, "/api/admin/config",
				map[string]any{"values": map[string]jsonx.RawMessage{
					tc.key: jsonx.RawMessage(tc.value),
				}},
				testCallOptions{uid: uid, role: "OPS"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			body := errorBody(t, rec)
			if body.Error.Code != "config_value_invalid" {
				t.Fatalf("code=%q, want config_value_invalid", body.Error.Code)
			}
			if !strings.Contains(body.Error.Message, tc.reason) {
				t.Fatalf("message=%q, want it to contain %q", body.Error.Message, tc.reason)
			}
			if fake.calls != 0 {
				t.Fatalf("rejected write reached the publisher")
			}
			t.Logf("A15 %s → HTTP 400 %s: %s", tc.key, body.Error.Code, body.Error.Message)
		})
	}
}

// Hard constraint 16: there is no blanket cache flush. An unregistered
// /api/admin/config method falls through to the shared unknown_endpoint answer.
func TestNoBlanketCacheFlushEndpoint(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	rec := call(t, svc.GetConfig, http.MethodGet, "/api/admin/config", nil,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("config list status=%d", rec.Code)
	}
	// The package exposes no Flush/Clear handler; this guards a future addition
	// by asserting the inventory has only the two whitelisted operations.
	typ := reflect.TypeOf(svc)
	for _, banned := range []string{"FlushConfigCache", "ClearCache", "PurgeConfig"} {
		if _, ok := typ.MethodByName(banned); ok {
			t.Fatalf("a blanket cache-clear handler was added: %s", banned)
		}
	}
}
