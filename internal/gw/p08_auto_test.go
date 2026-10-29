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
	"sync/atomic"
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
	return autoDedupFixtureAt(t, window, unaryBody,
		redisx.NewStaticClock(autoTestNow), basicCatalog())
}

// autoDedupFixtureAt is autoDedupFixture with an explicit clock and catalog, so
// a case can move time across a bucket boundary or offer a second model.
func autoDedupFixtureAt(t *testing.T, window time.Duration, unaryBody string,
	clock *redisx.Clock, catalog *Catalog) (*Handler, *autoIdemFake, *fakeLedger, *int) {
	t.Helper()
	deps := serverDeps(catalog, authorizedAuth("sk-auto"))
	deps.Clock = clock
	store := newAutoIdemFake(window)
	deps.Idem = store
	calls := 0
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, r *http.Request, c *Ctx) {
		calls++
		body := []byte(unaryBody)
		if c.Stream {
			body = nil
		}
		if c.Idem != nil {
			// Mirrors dispatch.completeIdem: with the window off P08 leaves
			// c.Idem nil and there is no lease to complete.
			_ = store.Complete(r.Context(), c.Idem.Record, idemstore.StateDone, body)
			c.IdemDone = true
		}
		w.WriteHeader(http.StatusOK)
		if !c.Stream {
			_, _ = w.Write(body)
		}
	}))
	return handler, store, deps.Ledger.(*fakeLedger), &calls
}

// autoTwoModelCatalog is basicCatalog plus a second listed model, so the model
// component of the synthetic key can be exercised end to end.
func autoTwoModelCatalog() *Catalog {
	return staticCatalog(
		[]ModelEntry{
			{
				ID: 1, ModelName: "qwen-plus", Region: RegionDomestic, ModelType: ModelTypeText,
				ContextLength: 32768, MaxOutputTokens: 32768, ListingState: ListingListed,
				DefaultPriceRuleID: 11,
			},
			{
				ID: 2, ModelName: "qwen-max", Region: RegionDomestic, ModelType: ModelTypeText,
				ContextLength: 32768, MaxOutputTokens: 32768, ListingState: ListingListed,
				DefaultPriceRuleID: 11,
			},
		},
		[]RouteEntry{
			{
				ID: 1, ModelID: 1, ChannelID: 1, ProtocolID: 1,
				UpstreamModelName: "qwen-plus", Region: RegionDomestic,
				Weight: 100, Priority: 100, StreamSupported: true, Enabled: true,
			},
			{
				ID: 2, ModelID: 2, ChannelID: 1, ProtocolID: 1,
				UpstreamModelName: "qwen-max", Region: RegionDomestic,
				Weight: 100, Priority: 100, StreamSupported: true, Enabled: true,
			},
		},
		[]ChannelEntry{{
			ID: 1, Name: "ch-1", ProtocolID: 1, Region: RegionDomestic,
			Weight: 100, Priority: 100, GroupScopeMode: ScopeAll, Enabled: true,
			StreamSupported: true,
		}},
		map[string]GroupEntry{
			"standard": {Code: "standard", RatioBP: 10000, ModelScopeMode: ScopeAll,
				ChannelScopeMode: ScopeAll, Enabled: true},
		},
		map[string]KeyModelGrant{
			"10": {Mode: ScopeAll, ModelIDs: []int64{1, 2}},
		},
	)
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

// Case ③: a duplicate that arrives while the first request is still executing
// takes the ActionWait branch — 409 duplicate_request_in_flight with the
// store's wait hint, and neither a second upstream call nor a second prehold.
func TestAutoDedupInFlightDuplicateIs409(t *testing.T) {
	const body = `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`
	const cached = `{"id":"first-response"}`
	deps := serverDeps(basicCatalog(), authorizedAuth("sk-auto"))
	deps.Clock = redisx.NewStaticClock(autoTestNow)
	store := newAutoIdemFake(10 * time.Second)
	deps.Idem = store

	var terminalCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, r *http.Request, c *Ctx) {
		// Hold the first request inside the terminal so the second one reaches
		// P08 while the lease is still IN_FLIGHT. A second arrival only happens
		// if de-duplication is broken; do not close started twice or block it.
		if terminalCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		if c.Idem != nil {
			_ = store.Complete(r.Context(), c.Idem.Record, idemstore.StateDone, []byte(cached))
			c.IdemDone = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(cached))
	}))
	ledger := deps.Ledger.(*fakeLedger)

	var first *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		first = autoPost(handler, body, "rid-auto-inflight-1")
	}()
	<-started

	// Release and join before asserting, so a red assertion cannot strand the
	// first request inside its terminal.
	second := autoPost(handler, body, "rid-auto-inflight-2")
	close(release)
	wg.Wait()

	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409 (body %s)", second.Code, second.Body.String())
	}
	eb := errorBody(t, decodeRecorderBody(t, second))
	if got := eb["code"]; got != "duplicate_request_in_flight" {
		t.Fatalf("code = %v, want duplicate_request_in_flight", got)
	}
	// The store's ActionWait carries a 1s hint; it is not the window remainder.
	if got := second.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "true" {
		t.Fatalf("X-Idempotent-Replay = %q, want true", got)
	}
	if got := terminalCalls.Load(); got != 1 {
		t.Fatalf("terminal ran %d times, want 1", got)
	}
	if got := ledger.preholdCount(); got != 1 {
		t.Fatalf("preholds = %d, want 1", got)
	}
}

// Case ④: buckets are fixed, not sliding. The same body one window later lands
// in the next floor(now/window) bucket and is executed again.
func TestAutoDedupAcrossBucketBoundaryExecutesTwice(t *testing.T) {
	const unary = `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`
	const cached = `{"id":"first-response"}`
	clock, setNow := redisx.NewAdjustableClock(autoTestNow)
	handler, store, ledger, calls := autoDedupFixtureAt(t, 10*time.Second, cached, clock, basicCatalog())

	first := autoPost(handler, unary, "rid-auto-bucket-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}
	setNow(autoTestNow.Add(10 * time.Second))
	second := autoPost(handler, unary, "rid-auto-bucket-2")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "" {
		t.Fatalf("cross-bucket request must not be marked as a replay, got %q", got)
	}
	if *calls != 2 {
		t.Fatalf("terminal ran %d times, want 2", *calls)
	}
	if got := ledger.preholdCount(); got != 2 {
		t.Fatalf("preholds = %d, want 2", got)
	}

	inputs := store.beginInputs()
	if len(inputs) != 2 {
		t.Fatalf("Begin calls = %d, want 2", len(inputs))
	}
	if inputs[0].BodyHash != inputs[1].BodyHash {
		t.Fatalf("body hash changed across the boundary: %q vs %q",
			inputs[0].BodyHash, inputs[1].BodyHash)
	}
	if inputs[0].IdemKey == inputs[1].IdemKey {
		t.Fatalf("next bucket reused the key %q", inputs[0].IdemKey)
	}
}

// Case ⑤a: a different body in the same bucket is a different request.
func TestAutoDedupDifferentBodyNotDeduped(t *testing.T) {
	const cached = `{"id":"first-response"}`
	handler, store, ledger, calls := autoDedupFixture(t, 10*time.Second, cached)

	first := autoPost(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"one"}]}`, "rid-auto-body-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}
	second := autoPost(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"two"}]}`, "rid-auto-body-2")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "" {
		t.Fatalf("different body must not be marked as a replay, got %q", got)
	}
	if *calls != 2 {
		t.Fatalf("terminal ran %d times, want 2", *calls)
	}
	if got := ledger.preholdCount(); got != 2 {
		t.Fatalf("preholds = %d, want 2", got)
	}

	inputs := store.beginInputs()
	if len(inputs) != 2 {
		t.Fatalf("Begin calls = %d, want 2", len(inputs))
	}
	if inputs[0].BodyHash == inputs[1].BodyHash {
		t.Fatal("test bodies produced the same hash")
	}
	if inputs[0].IdemKey == inputs[1].IdemKey {
		t.Fatalf("different bodies shared the key %q", inputs[0].IdemKey)
	}
}

// Case ⑤b: a different model in the same bucket is a different request. The
// HTTP body necessarily differs too; the direct key assertion below pins the
// model component itself, which the body hash cannot isolate.
func TestAutoDedupDifferentModelNotDeduped(t *testing.T) {
	const cached = `{"id":"first-response"}`
	handler, _, ledger, calls := autoDedupFixtureAt(t, 10*time.Second, cached,
		redisx.NewStaticClock(autoTestNow), autoTwoModelCatalog())

	first := autoPost(handler,
		`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`, "rid-auto-model-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}
	second := autoPost(handler,
		`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`, "rid-auto-model-2")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "" {
		t.Fatalf("different model must not be marked as a replay, got %q", got)
	}
	if *calls != 2 {
		t.Fatalf("terminal ran %d times, want 2", *calls)
	}
	if got := ledger.preholdCount(); got != 2 {
		t.Fatalf("preholds = %d, want 2", got)
	}

	// Same user, key, body hash and bucket; only ModelName differs.
	base := &Ctx{
		Auth:  &AuthContext{UserID: 7, KeyID: 10},
		Model: &ModelEntry{ID: 1, ModelName: "qwen-plus"},
	}
	other := *base
	other.Model = &ModelEntry{ID: 2, ModelName: "qwen-max"}
	nowMS := autoTestNow.UnixMilli()
	k1 := autoIdemKey(base, "same-body-hash", nowMS, 10*time.Second)
	k2 := autoIdemKey(&other, "same-body-hash", nowMS, 10*time.Second)
	if k1 == k2 {
		t.Fatalf("model name does not contribute to the key: %q", k1)
	}
}

// Case ⑥: window 0 is the configuration switch. P08 is skipped entirely, so
// two identical requests both execute and the store is never touched.
func TestAutoDedupWindowZeroDisablesStage(t *testing.T) {
	const unary = `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`
	const cached = `{"id":"first-response"}`
	handler, store, ledger, calls := autoDedupFixture(t, 0, cached)

	first := autoPost(handler, unary, "rid-auto-off-1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body %s", first.Code, first.Body.String())
	}
	second := autoPost(handler, unary, "rid-auto-off-2")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body %s", second.Code, second.Body.String())
	}
	if got := second.Header().Get("X-Idempotent-Replay"); got != "" {
		t.Fatalf("window 0 must not mark a replay, got %q", got)
	}
	if *calls != 2 {
		t.Fatalf("terminal ran %d times, want 2", *calls)
	}
	if got := ledger.preholdCount(); got != 2 {
		t.Fatalf("preholds = %d, want 2", got)
	}
	if got := store.beginInputs(); len(got) != 0 {
		t.Fatalf("Begin calls = %d, want 0 (P08 must be skipped)", len(got))
	}
}
