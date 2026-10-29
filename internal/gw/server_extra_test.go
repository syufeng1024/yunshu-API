package gw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yunshu.dev/core/internal/breaker"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/jsonx"
)

type fakeIdem struct {
	mu        sync.Mutex
	result    idemstore.BeginResult
	begins    int
	completes []idemstore.State
	// window is idem.duplicate_window_seconds. The zero value disables the T41
	// auto-dedup path, so tests that exercise explicit keys are unaffected.
	window time.Duration
}

func (f *fakeIdem) DuplicateWindow() time.Duration { return f.window }

func (f *fakeIdem) Begin(context.Context, idemstore.BeginInput) (idemstore.BeginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins++
	return f.result, nil
}

func (f *fakeIdem) Complete(_ context.Context, _ idemstore.Record, state idemstore.State, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completes = append(f.completes, state)
	return nil
}

func idemHandler(t *testing.T, result idemstore.BeginResult) (*Handler, *fakeIdem) {
	t.Helper()
	key := "sk-idem"
	deps := serverDeps(basicCatalog(), authorizedAuth(key))
	fake := &fakeIdem{result: result}
	deps.Idem = fake
	return NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
		w.WriteHeader(http.StatusOK)
	})), fake
}

func idemRequest(handler http.Handler, body, rid string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-idem")
	req.Header.Set("Idempotency-Key", "idem-1")
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestServerIdemReplayWritesCachedBodyAndSkipsTerminal(t *testing.T) {
	cached := []byte(`{"id":"cached"}`)
	handler, fake := idemHandler(t, idemstore.BeginResult{
		Action: idemstore.ActionReplay,
		Record: &idemstore.Record{RequestID: "original", ResponseBody: cached},
	})
	rec := idemRequest(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`, "rid-new")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"id":"cached"}` {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") != "original" {
		t.Fatalf("replay must keep the original request id, got %q", rec.Header().Get("X-Request-Id"))
	}
	if fake.completes != nil {
		t.Fatalf("a replay must not complete the record: %v", fake.completes)
	}
}

func TestServerIdemKeyReusedIs409(t *testing.T) {
	handler, _ := idemHandler(t, idemstore.BeginResult{Action: idemstore.ActionKeyReused})
	rec := idemRequest(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`, "rid-reuse")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errorBody(t, body)["code"] != "idempotency_key_reused" {
		t.Fatalf("body = %v", body)
	}
}

func TestServerIdemInFlightIs409WithRetryAfter(t *testing.T) {
	handler, _ := idemHandler(t, idemstore.BeginResult{
		Action:     idemstore.ActionWait,
		RetryAfter: 1,
	})
	rec := idemRequest(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`, "rid-wait")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q, want 2", rec.Header().Get("Retry-After"))
	}
}

func TestServerOwnedIdemLeaseIsCompletedFailedOnEarlyExit(t *testing.T) {
	handler, fake := idemHandler(t, idemstore.BeginResult{
		Action: idemstore.ActionNew,
		Record: &idemstore.Record{RequestID: "owned"},
	})
	// Price source absent -> P09 fails with an internal 500 after P08 owns the
	// lease. The exit path must mark it FAILED so a retry is allowed.
	handler.d.Prices = fakePrices{err: context.DeadlineExceeded}
	rec := idemRequest(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`, "rid-owned")
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the pipeline to fail, got 200")
	}
	if len(fake.completes) != 1 || fake.completes[0] != idemstore.StateFailed {
		t.Fatalf("completes = %v, want [FAILED]", fake.completes)
	}
}

// T08 removed the temporary terminal. The handler must hand a successful
// pipeline to the injected terminal and must not settle on its own: settlement
// belongs to the dispatcher, which knows the real usage.
func TestHandlerDelegatesToTerminalAndDoesNotSettle(t *testing.T) {
	key := "sk-ok"
	deps := serverDeps(basicCatalog(), authorizedAuth(key))
	fake := &fakeLedger{hold: 10}
	deps.Ledger = fake
	called := false
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, c *Ctx) {
		called = true
		if c.Hold == nil || c.Hold.HoldMicro != 10 {
			t.Errorf("terminal did not receive the hold: %+v", c.Hold)
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec, _ := postChat(t, handler, key, "qwen-plus", "rid-terminal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("the terminal was not called")
	}
	if len(fake.settles) != 0 {
		t.Fatalf("the handler must not settle; settles = %v", fake.settles)
	}
}

func TestDepsValidateReportsMissingComponents(t *testing.T) {
	if err := (*Deps)(nil).Validate(); err == nil {
		t.Fatal("nil deps validated")
	}
	d := &Deps{}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("error = %v", err)
	}

	full := staticDeps(nil)
	full.Auth = fakeAuth{}
	full.Prices = fakePrices{}
	full.Balance = fakeBalance{}
	full.Ledger = &fakeLedger{}
	full.Limiter = &fakeLimiter{}
	if err := full.Validate(); err != nil {
		t.Fatalf("complete deps rejected: %v", err)
	}
}

func TestConnGuardCapsPerKey(t *testing.T) {
	g := NewConnGuard(nil)
	// concurrency 1 => cap 2.
	if !g.Acquire(t.Context(), 10, 1) || !g.Acquire(t.Context(), 10, 1) {
		t.Fatal("first two acquisitions rejected")
	}
	if g.Acquire(t.Context(), 10, 1) {
		t.Fatal("third acquisition admitted beyond 2× concurrency")
	}
	if g.Count(10) != 2 {
		t.Fatalf("count = %d", g.Count(10))
	}
	g.Release(10)
	g.Release(10)
	if g.Count(10) != 0 {
		t.Fatalf("count after release = %d", g.Count(10))
	}
}

func TestBalanceSourceReadsAvailableMicro(t *testing.T) {
	userID := seedAccount(t, 6500)
	source := DBAccountSource{DB: testDB}
	micro, found, err := source.LoadBalance(t.Context(), userID)
	if err != nil || !found || micro != 6500 {
		t.Fatalf("LoadBalance = %d, %v, %v; want 6500,true,nil", micro, found, err)
	}
	_, found, err = source.LoadBalance(t.Context(), 2_000_000_000)
	if err != nil || found {
		t.Fatalf("missing account = %v, %v", found, err)
	}
}

type fakeBreaker struct {
	state     breaker.State
	openUntil int64
}

func (f fakeBreaker) State(context.Context, int64) (breaker.State, int64, error) {
	return f.state, f.openUntil, nil
}

func TestRouterSkipsOpenBreakerChannels(t *testing.T) {
	catalog := basicCatalog()
	deps := staticDeps(catalog)
	deps.Breaker = fakeBreaker{state: breaker.StateOpen, openUntil: 1 << 62}
	c := &Ctx{Model: &ModelEntry{ID: 1}, Auth: &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"}}
	err := NewRouterStage(deps).Run(t.Context(), c)
	assertAPIError(t, err, "no_available_upstream", http.StatusServiceUnavailable)

	deps.Breaker = fakeBreaker{state: breaker.StateOpen, openUntil: 0}
	c = &Ctx{Model: &ModelEntry{ID: 1}, Auth: &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"}}
	if err := NewRouterStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("expired OPEN must be admitted: %v", err)
	}
}
