package gw

// T41 segment 1: automatic de-duplication for requests without an
// Idempotency-Key. The store double below keys rows exactly like the
// t_idempotency_record unique key (scope, api_key_id, idem_key), so the two
// cases fail if the synthetic key or its scope drifts.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/redisx"
)

// autoTestNow sits exactly on a 10s bucket boundary, so the synthetic key is
// stable across both requests and the remaining window is the full 10s.
var autoTestNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

type autoIdemFake struct {
	mu      sync.Mutex
	window  time.Duration
	records map[string]idemstore.Record
	begins  []idemstore.BeginInput
}

func newAutoIdemFake(window time.Duration) *autoIdemFake {
	return &autoIdemFake{window: window, records: make(map[string]idemstore.Record)}
}

func (f *autoIdemFake) DuplicateWindow() time.Duration { return f.window }

func (f *autoIdemFake) Begin(_ context.Context, in idemstore.BeginInput) (idemstore.BeginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins = append(f.begins, in)
	k := in.Scope + "|" + in.IdemKey
	if rec, ok := f.records[k]; ok {
		out := rec
		switch rec.State {
		case idemstore.StateDone:
			return idemstore.BeginResult{Action: idemstore.ActionReplay, Record: &out}, nil
		case idemstore.StateInFlight:
			return idemstore.BeginResult{Action: idemstore.ActionWait, RetryAfter: time.Second, Record: &out}, nil
		}
	}
	rec := idemstore.Record{
		Scope: in.Scope, APIKeyID: in.APIKeyID, UserID: in.UserID,
		IdemKey: in.IdemKey, BodyHash: in.BodyHash,
		State: idemstore.StateInFlight, RequestID: in.RequestID,
	}
	f.records[k] = rec
	return idemstore.BeginResult{Action: idemstore.ActionNew, Record: &rec}, nil
}

func (f *autoIdemFake) Complete(_ context.Context, rec idemstore.Record, state idemstore.State, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec.State = state
	rec.ResponseBody = append([]byte(nil), body...)
	f.records[rec.Scope+"|"+rec.IdemKey] = rec
	return nil
}

func (f *autoIdemFake) beginInputs() []idemstore.BeginInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]idemstore.BeginInput, len(f.begins))
	copy(out, f.begins)
	return out
}

// autoDedupFixture builds a full handler whose terminal mimics T08: it records
// DONE with the unary body (streams store no body) before returning 200.
func autoDedupFixture(t *testing.T, window time.Duration, unaryBody string) (*Handler, *autoIdemFake, *fakeLedger, *int) {
	t.Helper()
	deps := serverDeps(basicCatalog(), authorizedAuth("sk-auto"))
	deps.Clock = redisx.NewStaticClock(autoTestNow)
	store := newAutoIdemFake(window)
	deps.Idem = store
	calls := 0
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, r *http.Request, c *Ctx) {
		calls++
		body := []byte(unaryBody)
		if c.Stream {
			body = nil
		}
		_ = store.Complete(r.Context(), c.Idem.Record, idemstore.StateDone, body)
		c.IdemDone = true
		w.WriteHeader(http.StatusOK)
		if !c.Stream {
			_, _ = w.Write(body)
		}
	}))
	return handler, store, deps.Ledger.(*fakeLedger), &calls
}

// autoPost sends a data-plane request with no Idempotency-Key.
func autoPost(handler http.Handler, body, rid string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-auto")
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// Case ①: the second identical unary request returns the cached body with
// X-Idempotent-Replay and never reaches P09/P10, so it is charged once.
func TestAutoDedupUnaryReplaysCachedBodyAndChargesOnce(t *testing.T) {
	const unary = `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`
	const cached = `{"id":"first-response"}`
	handler, store, ledger, calls := autoDedupFixture(t, 10*time.Second, cached)

	first := autoPost(handler, unary, "rid-auto-first")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-Idempotent-Replay"); got != "" {
		t.Fatalf("first response must not be marked as a replay, got %q", got)
	}

	second := autoPost(handler, unary, "rid-auto-second")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body %s", second.Code, second.Body.String())
	}
	if got := strings.TrimSpace(second.Body.String()); got != cached {
		t.Fatalf("replayed body = %q, want %q", got, cached)
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "true" {
		t.Fatalf("X-Idempotent-Replay = %q, want true", got)
	}
	if *calls != 1 {
		t.Fatalf("terminal ran %d times, want 1", *calls)
	}
	// P10 is the only prehold in the pipeline. A replay that reached it would
	// reserve funds a second time.
	if got := ledger.preholdCount(); got != 1 {
		t.Fatalf("preholds = %d, want 1", got)
	}

	inputs := store.beginInputs()
	if len(inputs) != 2 {
		t.Fatalf("Begin calls = %d, want 2", len(inputs))
	}
	if idemAutoScope == idemScope {
		t.Fatal("automatic scope must be isolated from the explicit-key scope")
	}
	for _, in := range inputs {
		if in.Scope != idemAutoScope {
			t.Fatalf("scope = %q, want %q", in.Scope, idemAutoScope)
		}
		if !strings.HasPrefix(in.IdemKey, idemAutoPrefix) {
			t.Fatalf("idem_key = %q, want %s prefix", in.IdemKey, idemAutoPrefix)
		}
		if len(in.IdemKey) > idemKeyMaxLen {
			t.Fatalf("idem_key length = %d, want <= %d", len(in.IdemKey), idemKeyMaxLen)
		}
	}
	if inputs[0].IdemKey != inputs[1].IdemKey {
		t.Fatalf("same body in one bucket produced different keys: %q vs %q",
			inputs[0].IdemKey, inputs[1].IdemKey)
	}
}

// Case ②: a stream hit cannot be replayed, so it is a 409
// duplicate_request_in_flight and the upstream is not called again.
func TestAutoDedupStreamHitIs409(t *testing.T) {
	const streamBody = `{"model":"qwen-plus","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	handler, _, ledger, calls := autoDedupFixture(t, 10*time.Second, `{"unused":true}`)

	first := autoPost(handler, streamBody, "rid-auto-stream-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}

	second := autoPost(handler, streamBody, "rid-auto-stream-2")
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409 (body %s)", second.Code, second.Body.String())
	}
	eb := errorBody(t, decodeRecorderBody(t, second))
	if got := eb["code"]; got != "duplicate_request_in_flight" {
		t.Fatalf("code = %v, want duplicate_request_in_flight", got)
	}
	if got := second.Header().Get("Retry-After"); got != "10" {
		t.Fatalf("Retry-After = %q, want 10 (remaining window at a bucket start)", got)
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "true" {
		t.Fatalf("X-Idempotent-Replay = %q, want true", got)
	}
	if *calls != 1 {
		t.Fatalf("terminal ran %d times, want 1", *calls)
	}
	if got := ledger.preholdCount(); got != 1 {
		t.Fatalf("preholds = %d, want 1", got)
	}
}
