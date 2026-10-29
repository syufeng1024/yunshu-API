package gw

// T39 segment 2 (OI-07): gw-side triggers for error codes that had a producer
// but no automated case. Codes are asserted by value; each case goes red when
// its production branch is removed.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/redisx"
)

func decodeRecorderBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode body (%d %s): %v", rec.Code, rec.Body.String(), err)
	}
	return decoded
}

// key_disabled (p05_auth.go:54). The three states are asserted side by side so
// a regression that collapses key_disabled into invalid_api_key or key_expired
// cannot pass by sharing a status code.
func TestKeyDisabledIs403AndDistinctFromInvalidAndExpired(t *testing.T) {
	const (
		disabledKey = "sk-disabled"
		expiredKey  = "sk-expired"
	)
	records := map[string]redisx.KeyAuthRecord{
		KeyHash16(disabledKey): {UID: 7, KID: 10, ST: "DISABLED", GRP: "standard", GP: 10000},
		KeyHash16(expiredKey): {UID: 7, KID: 10, ST: "ENABLED", GRP: "standard", GP: 10000,
			EXP: time.Now().Add(-time.Minute).UnixMilli()},
	}
	handler := NewHandler(serverDeps(basicCatalog(), fakeAuth{records: records}), noopTerminal)

	cases := []struct {
		name   string
		key    string
		status int
		code   string
	}{
		{name: "unknown hash", key: "sk-never-cached", status: http.StatusUnauthorized, code: "invalid_api_key"},
		{name: "disabled", key: disabledKey, status: http.StatusForbidden, code: "key_disabled"},
		{name: "expired", key: expiredKey, status: http.StatusUnauthorized, code: "key_expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, decoded := postChat(t, handler, tc.key, "qwen-plus", "rid-"+tc.name)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if got := errorBody(t, decoded)["code"]; got != tc.code {
				t.Fatalf("code = %v, want %s (body %s)", got, tc.code, rec.Body.String())
			}
		})
	}
}

// idempotent_replay_unsupported (server.go:278): P08 decides
// response_not_replayable / stream_response_not_replayable when a DONE record
// has no stored body. Both decision strings map to the same outward code; the
// table keeps both case labels covered. The companion test
// TestLargeUnaryResponseIsNotStoredForReplay in package dispatch proves the
// >64 KiB truncation that produces the bodyless record.
func TestIdempotentReplayWithoutStoredBodyIs409(t *testing.T) {
	const key = "sk-idem"
	bodies := []struct {
		name string
		body string
	}{
		{"non-stream", `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`},
		{"stream", `{"model":"qwen-plus","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			deps := serverDeps(basicCatalog(), authorizedAuth(key))
			deps.Idem = &fakeIdem{result: idemstore.BeginResult{
				Action: idemstore.ActionReplay,
				Record: &idemstore.Record{RequestID: "original"},
			}}
			called := false
			handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			rec := idemRequest(handler, tc.body, "rid-replay-"+tc.name)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
			}
			if got := errorBody(t, decodeRecorderBody(t, rec))["code"]; got != "idempotent_replay_unsupported" {
				t.Fatalf("code = %v, want idempotent_replay_unsupported (body %s)", got, rec.Body.String())
			}
			if called {
				t.Fatal("a replay decision must not run the upstream terminal")
			}
		})
	}
}
