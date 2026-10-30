package dispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/billing"
	"yunshu.dev/core/internal/cryptobox"
	"yunshu.dev/core/internal/fakeupstream"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/stream"
	"yunshu.dev/relaykit/ir"
)

// --- fakes ---------------------------------------------------------------

type fakeLedger struct {
	mu      sync.Mutex
	settles []ledger.SettleParams
	err     error
}

func (f *fakeLedger) Prehold(context.Context, ledger.PreholdParams) (ledger.PreholdResult, error) {
	return ledger.PreholdResult{HoldMicro: 1000, AvailableBeforeMicro: 10000}, nil
}

func (f *fakeLedger) Settle(_ context.Context, p ledger.SettleParams) (ledger.SettleResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settles = append(f.settles, p)
	if f.err != nil {
		return ledger.SettleResult{}, f.err
	}
	return ledger.SettleResult{
		RequestID:     p.RequestID,
		State:         ledger.HoldStateSettled,
		ConsumedMicro: p.ConsumedMicro,
		PreholdMicro:  1000,
		ReleasedMicro: 1000 - p.ConsumedMicro,
	}, nil
}

func (f *fakeLedger) settleCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.settles)
}

func (f *fakeLedger) lastSettle() ledger.SettleParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.settles) == 0 {
		return ledger.SettleParams{}
	}
	return f.settles[len(f.settles)-1]
}

type fakeIdem struct {
	completes []idemstore.State
	bodies    [][]byte
	// window is idem.duplicate_window_seconds (T41). The zero value disables
	// the T41 auto-dedup path for requests without an Idempotency-Key, so the
	// existing dispatch fixtures keep their exact behaviour.
	window time.Duration
}

func (f *fakeIdem) DuplicateWindow() time.Duration { return f.window }

func (f *fakeIdem) Begin(context.Context, idemstore.BeginInput) (idemstore.BeginResult, error) {
	return idemstore.BeginResult{}, nil
}

func (f *fakeIdem) Complete(_ context.Context, _ idemstore.Record, state idemstore.State, body []byte) error {
	f.completes = append(f.completes, state)
	f.bodies = append(f.bodies, body)
	return nil
}

type counterFunc func(string) int64

func (c counterFunc) CountInput(text string) (int64, error) { return c(text), nil }

type fakeCooldown struct {
	mu      sync.Mutex
	seconds time.Duration
}

func (f *fakeCooldown) SetCooldown(_ context.Context, _ int64, d time.Duration) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seconds = d
	return d, nil
}

type fakeBalance struct{ micro int64 }

func (b fakeBalance) Get(context.Context, int64) (int64, error) { return b.micro, nil }

func ptrInt64(v int64) *int64 { return &v }

// testConfigVersion is the ROUTING snapshot version the dispatcher fixtures
// carry. In production gw.Handler stamps the same value on gw.Ctx at ingress.
const testConfigVersion int64 = 42

func testBox(t *testing.T) (*cryptobox.Box, string) {
	t.Helper()
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 7)
	}
	box, err := cryptobox.New(kek)
	if err != nil {
		t.Fatalf("cryptobox: %v", err)
	}
	envelope, err := box.SealString("sk-upstream-test")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return box, envelope
}

func testCatalog(t *testing.T, baseURL, envelope string, mutate func(*gw.ChannelEntry)) *gw.Catalog {
	t.Helper()
	ch := gw.ChannelEntry{
		ID: 10, Name: "fake", ProtocolID: 2, Region: gw.RegionDomestic,
		Weight: 100, Priority: 100, GroupScopeMode: gw.ScopeAll,
		DedicatedScopeMode: gw.ScopeAll, StreamSupported: true, Enabled: true,
		BaseURL: baseURL, UpstreamKeyEnc: envelope,
		TimeoutConnectMS: 1000, TimeoutFirstByteMS: 1000, TimeoutTotalMS: 5000,
		RetryMax: ptrInt64(1), StreamUsageMode: "UNKNOWN",
		UpstreamModelNameMap: map[string]string{},
	}
	if mutate != nil {
		mutate(&ch)
	}
	return gw.NewStaticCatalog(&gw.RoutingSnapshot{
		Version: testConfigVersion,
		Models: []gw.ModelEntry{{
			ID: 1, ModelName: "m", Region: gw.RegionDomestic, ModelType: gw.ModelTypeText,
			ContextLength: 32768, MaxOutputTokens: 32, ListingState: gw.ListingListed,
		}},
		Routes: []gw.RouteEntry{{
			ID: 1, ModelID: 1, ChannelID: 10, ProtocolID: 2,
			UpstreamModelName: "fake-model", Region: gw.RegionDomestic,
			Weight: 100, Priority: 100, StreamSupported: true, Enabled: true,
		}},
		Channels: []gw.ChannelEntry{ch},
		Groups: map[string]gw.GroupEntry{
			"standard": {Code: "standard", RatioBP: 10000, ModelScopeMode: gw.ScopeAll,
				ChannelScopeMode: gw.ScopeAll, Enabled: true},
		},
		KeyModels: map[string]gw.KeyModelGrant{"10": {Mode: gw.ScopeAll, ModelIDs: []int64{1}}},
	})
}

func testCtx(streaming bool) *gw.Ctx {
	rid := "rid-" + time.Now().Format("150405.000000")
	route := gw.RouteCandidate{
		RouteID: 1, ChannelID: 10, ProtocolID: 2, UpstreamModelName: "fake-model",
		Priority: 100, Weight: 100, StreamSupported: true,
	}
	maxTokens := int64(16)
	now := time.Now()
	return &gw.Ctx{
		RequestID: rid, StartedAt: now, Deadline: now.Add(120 * time.Second),
		Stream: streaming, Endpoint: gw.EndpointChat, Protocol: gw.ProtocolOpenAI,
		IR: &ir.Request{
			Model: "m", Stream: streaming, MaxTokens: &maxTokens,
			Messages: []ir.Message{{Role: "user", Content: ir.Content{Text: "hi"}}},
		},
		Auth:  &gw.AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard", GroupRatioBP: 10000},
		Model: &gw.ModelEntry{ID: 1, ModelName: "m", ContextLength: 32768, MaxOutputTokens: 32, ListingState: gw.ListingListed},
		Route: &route, Routes: []gw.RouteCandidate{route},
		Est: &billing.Preestimate{
			Tokens:   billing.TokenEstimate{InputTokens: 5, OutputTokens: 4},
			RawMicro: 100, EstMicro: 120,
		},
		Price:     &gw.PriceRule{InPriceMicroPerK: 1000, OutPriceMicroPerK: 2000},
		Hold:      &ledger.PreholdResult{RequestID: rid, HoldMicro: 1000, AvailableBeforeMicro: 10000, State: ledger.HoldStateHeld},
		HoldMicro: 1000, AvailableMicro: 10000,
	}
}

type attemptRecorder struct {
	mu   sync.Mutex
	list []Attempt
}

func (r *attemptRecorder) add(a Attempt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, a)
}

func (r *attemptRecorder) all() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Attempt(nil), r.list...)
}

func newDispatcher(t *testing.T, cat *gw.Catalog, led *fakeLedger, rec *attemptRecorder, idem *fakeIdem) *Dispatcher {
	t.Helper()
	box, _ := testBox(t)
	return New(Options{
		Catalog: cat,
		Tokens:  counterFunc(func(s string) int64 { return int64(len(s)) }),
		Ledger:  led,
		Balance: fakeBalance{micro: 10000},
		KEK:     box,
		Idem:    idem,
		AttemptSink: func(a Attempt) {
			rec.add(a)
		},
	})
}

func serve(t *testing.T, d *Dispatcher, c *gw.Ctx) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(context.Background())
	d.Serve(rec, req, c)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	return body.Error
}

// --- A3 ------------------------------------------------------------------

func TestA3Upstream429Becomes503WithoutRetry(t *testing.T) {
	up := httptest.NewServer(fakeupstream.New(fakeupstream.Options{
		Mode: fakeupstream.Mode429, RetryAfter: "30",
	}))
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})
	cool := &fakeCooldown{}
	d.opts.Cooldown = cool

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	body := errorCode(t, rec)
	if body["code"] != apierr.CodeUpstreamCapacityExceeded {
		t.Fatalf("code = %v", body["code"])
	}
	if body["retry_after_seconds"] != float64(30) {
		t.Fatalf("retry_after_seconds = %v", body["retry_after_seconds"])
	}
	if rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	attempts := recs.all()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].HTTPStatus != 429 || attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("attempt = %+v", attempts[0])
	}
	if led.settleCount() != 1 || led.lastSettle().ConsumedMicro != 0 {
		t.Fatalf("settles = %+v", led.settles)
	}
	if cool.seconds != 30*time.Second {
		t.Fatalf("cooldown TTL = %v, want 30s", cool.seconds)
	}
	t.Logf("attempts=%d outward_status=%d outward_code=%v retry_after=%v cooldown=%v", len(attempts), rec.Code, body["code"], body["retry_after_seconds"], cool.seconds)
}

// --- A4 ------------------------------------------------------------------

func TestA4Upstream500RetriesOnceAndSettlesOnce(t *testing.T) {
	up := httptest.NewServer(fakeupstream.New(fakeupstream.Options{
		Mode: fakeupstream.ModeFailFirst, FailFirst: 1,
	}))
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	for _, a := range recs.all() {
		t.Logf("attempt %d status=%d class=%s result=%s decision=%s msg=%s", a.AttemptIndex, a.HTTPStatus, a.ErrorClass, a.Result, a.RetryDecision, a.UpstreamMsg)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	attempts := recs.all()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].HTTPStatus != 500 || !attempts[0].IsRetryable ||
		attempts[0].RetryDecision == DecisionNoRetry {
		t.Fatalf("first attempt = %+v", attempts[0])
	}
	if attempts[1].HTTPStatus != 200 || attempts[1].Result != "SUCCESS" {
		t.Fatalf("second attempt = %+v", attempts[1])
	}
	if led.settleCount() != 1 {
		t.Fatalf("settles = %d, want exactly 1", led.settleCount())
	}
	if rec.Header().Get("X-Credits-Consumed") == "" {
		t.Fatalf("non-stream response must expose X-Credits-Consumed: %v", rec.Header())
	}
}

// --- A5 ------------------------------------------------------------------

func TestA5DNSFailureIsNotRetried(t *testing.T) {
	_, envelope := testBox(t)
	cat := testCatalog(t, "http://no-such-host.invalid/v1", envelope, func(ch *gw.ChannelEntry) {
		ch.TimeoutConnectMS = 300
		ch.TimeoutFirstByteMS = 300
		ch.TimeoutTotalMS = 500
	})
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	body := errorCode(t, rec)
	if body["code"] != apierr.CodeUpstreamDNSFailure {
		t.Fatalf("code = %v (want %s)", body["code"], apierr.CodeUpstreamDNSFailure)
	}
	attempts := recs.all()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].IsRetryable || attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("DNS must not be retryable: %+v", attempts[0])
	}
	t.Logf("attempts=%d outward_status=%d outward_code=%v class=%s", len(attempts), rec.Code, body["code"], attempts[0].ErrorClass)
}

// --- A6 ------------------------------------------------------------------

func TestA6BudgetBelowThresholdDoesNotRetry(t *testing.T) {
	up := httptest.NewServer(fakeupstream.New(fakeupstream.Options{
		Mode: fakeupstream.ModeSlow, SlowDelay: 100 * time.Millisecond,
	}))
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, func(ch *gw.ChannelEntry) {
		ch.TimeoutFirstByteMS = 20
	})
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	c := testCtx(false)
	c.Deadline = time.Now().Add(40 * time.Second) // < unary 65s threshold
	rec := serve(t, d, c)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body %s)", rec.Code, rec.Body.String())
	}
	body := errorCode(t, rec)
	if body["code"] != apierr.CodeUpstreamTimeout {
		t.Fatalf("code = %v", body["code"])
	}
	if attempts := recs.all(); len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 (40s remaining is below the 65s threshold)", len(attempts))
	} else {
		t.Logf("attempts=%d outward_status=%d outward_code=%v class=%s", len(attempts), rec.Code, body["code"], attempts[0].ErrorClass)
	}
}

// --- A10 -----------------------------------------------------------------

func TestA10ClientAbortSettlesProducedTokensAndReleasesTheRest(t *testing.T) {
	up := httptest.NewServer(fakeupstream.New(fakeupstream.Options{
		Mode: fakeupstream.ModeStreamStall,
	}))
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	c := testCtx(true)
	c.Deadline = time.Now().Add(600 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		d.Serve(rec, req, c)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not return after the client abort")
	}

	if led.settleCount() != 1 {
		t.Fatalf("settles = %d, want 1", led.settleCount())
	}
	settle := led.lastSettle()
	// 20 chunks × 10 characters = 200 output tokens; 5 input tokens.
	// cost = ceil((5×1000 + 200×2000)/1000) = 405.
	if settle.ConsumedMicro != 405 {
		t.Fatalf("consumed = %d, want 405 (produced 200 tokens)", settle.ConsumedMicro)
	}
	if released := 1000 - settle.ConsumedMicro; released != 595 {
		t.Fatalf("released = %d, want 595", released)
	} else {
		t.Logf("consumed=%d released=%d", settle.ConsumedMicro, released)
	}
	if settle.ReleaseReason == "CLIENT_ABORT" {
		t.Fatalf("partial settlement must be a normal settle, reason = %q", settle.ReleaseReason)
	}
	attempts := recs.all()
	if len(attempts) != 1 || attempts[0].Result != "PARTIAL" {
		t.Fatalf("attempt = %+v", attempts)
	}
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("an aborted stream must not append [DONE]: %s", rec.Body.String())
	}
}

// --- config / key --------------------------------------------------------

func TestMissingChannelPayloadIs503ConfigUnavailable(t *testing.T) {
	_, envelope := testBox(t)
	cat := testCatalog(t, "http://127.0.0.1:1/v1", envelope, func(ch *gw.ChannelEntry) {
		ch.UpstreamKeyEnc = "" // D-14: never guess a default
	})
	led := &fakeLedger{}
	d := newDispatcher(t, cat, led, &attemptRecorder{}, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if errorCode(t, rec)["code"] != apierr.CodeConfigUnavailable {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if led.settleCount() != 1 || led.lastSettle().ConsumedMicro != 0 {
		t.Fatalf("the hold must be released: %+v", led.settles)
	}
}

func TestPlaintextKeyIsNeverAccepted(t *testing.T) {
	_, envelope := testBox(t)
	_ = envelope
	cat := testCatalog(t, "http://127.0.0.1:1/v1", "sk-upstream-test", nil)
	led := &fakeLedger{}
	d := newDispatcher(t, cat, led, &attemptRecorder{}, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if errorCode(t, rec)["code"] != apierr.CodeConfigUnavailable {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestMissingKEKIs503NotPlaintext(t *testing.T) {
	_, envelope := testBox(t)
	cat := testCatalog(t, "http://127.0.0.1:1/v1", envelope, nil)
	d := New(Options{Catalog: cat, Tokens: counterFunc(func(string) int64 { return 1 }),
		Ledger: &fakeLedger{}, KEK: nil})
	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if errorCode(t, rec)["code"] != apierr.CodeUpstreamKeyUnavailable {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// T39: a channel whose upstream_key_enc exists (valid v1 envelope) but cannot
// be opened by the configured KEK must surface upstream_key_unavailable, and the
// usage row must attribute it to the platform's routing stage.
func TestUpstreamKeyUndecryptableIs503UpstreamKeyUnavailable(t *testing.T) {
	_, envelope := testBox(t) // sealed with the standard test KEK
	cat := testCatalog(t, "http://127.0.0.1:1/v1", envelope, nil)
	otherKEK := make([]byte, 32)
	for i := range otherKEK {
		otherKEK[i] = byte(i + 99)
	}
	box, err := cryptobox.New(otherKEK)
	if err != nil {
		t.Fatalf("cryptobox: %v", err)
	}
	usage := &usageRecorder{}
	d := New(Options{
		Catalog: cat,
		Tokens:  counterFunc(func(string) int64 { return 1 }),
		Ledger:  &fakeLedger{},
		KEK:     box,
	})
	d.opts.UsageSink = usage.add

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeUpstreamKeyUnavailable {
		t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeUpstreamKeyUnavailable, rec.Body.String())
	}
	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1", len(rows))
	}
	row := rows[0]
	if row.HTTPStatus != http.StatusServiceUnavailable || row.BizCode != apierr.CodeUpstreamKeyUnavailable {
		t.Fatalf("usage status/code = %d/%q", row.HTTPStatus, row.BizCode)
	}
	if row.FailStage != "ROUTE" || row.FailOwner != "PLATFORM" {
		t.Fatalf("usage attribution = %q/%q, want ROUTE/PLATFORM", row.FailStage, row.FailOwner)
	}
}

// T39 reverse: a routing/config problem must keep config_unavailable; the key
// change above must not have widened this path.
func TestMissingBaseURLStillConfigUnavailable(t *testing.T) {
	_, envelope := testBox(t)
	cat := testCatalog(t, "", envelope, nil)
	usage := &usageRecorder{}
	d := newDispatcher(t, cat, &fakeLedger{}, &attemptRecorder{}, &fakeIdem{})
	d.opts.UsageSink = usage.add

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeConfigUnavailable {
		t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeConfigUnavailable, rec.Body.String())
	}
	rows := usage.all()
	if len(rows) != 1 || rows[0].BizCode != apierr.CodeConfigUnavailable {
		t.Fatalf("usage rows = %+v", rows)
	}
}

// --- E-14 auto probe -----------------------------------------------------

type modeWriter struct {
	mu    sync.Mutex
	calls []string
}

func (w *modeWriter) SetStreamUsageMode(_ context.Context, channelID int64, mode string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, mode)
	return nil
}

func (w *modeWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

func TestE14AutoDowngradeAfterTwentyUsageMisses(t *testing.T) {
	box, _ := testBox(t)
	writer := &modeWriter{}
	d := New(Options{
		Catalog:         gw.NewStaticCatalog(&gw.RoutingSnapshot{}),
		Tokens:          counterFunc(func(string) int64 { return 1 }),
		KEK:             box,
		UsageModeWriter: writer,
	})
	for i := 0; i < probeThreshold; i++ {
		d.noteUsage(context.Background(), 10, false, "UNKNOWN")
	}
	if writer.count() != 1 || writer.calls[0] != "ESTIMATE" {
		t.Fatalf("writer calls = %v", writer.calls)
	}
	// A success resets the counter; a further miss must not downgrade again.
	d.noteUsage(context.Background(), 10, true, "UNKNOWN")
	d.noteUsage(context.Background(), 10, false, "UNKNOWN")
	if writer.count() != 1 {
		t.Fatalf("a second downgrade was attempted: %v", writer.calls)
	}
}

func TestE14NilWriterOnlyWarnsAndNeverPanics(t *testing.T) {
	box, _ := testBox(t)
	d := New(Options{
		Catalog: gw.NewStaticCatalog(&gw.RoutingSnapshot{}),
		Tokens:  counterFunc(func(string) int64 { return 1 }),
		KEK:     box,
	})
	var panicked atomic.Bool
	func() {
		defer func() {
			if recover() != nil {
				panicked.Store(true)
			}
		}()
		for i := 0; i < probeThreshold; i++ {
			d.noteUsage(context.Background(), 99, false, "UNKNOWN")
		}
	}()
	if panicked.Load() {
		t.Fatal("a nil write-back interface must not panic")
	}
}

// Keep imports honest when a test is temporarily skipped.
var _ = stream.SourceEstimate

// --- upstream body classification ---------------------------------------

func fixedUpstream(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestEmptyChoicesIs502AndNotBilled(t *testing.T) {
	up := fixedUpstream(http.StatusOK, `{"id":"x","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadGateway || errorCode(t, rec)["code"] != apierr.CodeUpstreamEmptyResponse {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if led.settleCount() != 1 || led.lastSettle().ConsumedMicro != 0 {
		t.Fatalf("an empty response must not be billed: %+v", led.settles)
	}
	if attempts := recs.all(); len(attempts) != 1 || attempts[0].ErrorClass != ClassEmptyChoices {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestMalformedBodyIs502WithoutRetry(t *testing.T) {
	up := fixedUpstream(http.StatusOK, `not-json`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, &fakeLedger{}, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadGateway || errorCode(t, rec)["code"] != apierr.CodeUpstreamInvalidResponse {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if attempts := recs.all(); len(attempts) != 1 || attempts[0].IsRetryable {
		t.Fatalf("malformed body must not retry: %+v", attempts)
	}
}

func TestUpstream400MapsTo400InvalidRequest(t *testing.T) {
	up := fixedUpstream(http.StatusBadRequest, `{"error":{"message":"bad parameter"}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, &fakeLedger{}, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadRequest || errorCode(t, rec)["code"] != apierr.CodeUpstreamInvalidRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if attempts := recs.all(); len(attempts) != 1 || attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestSettlementFailureAnswers503(t *testing.T) {
	up := fixedUpstream(http.StatusOK, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{err: errors.New("db down")}
	d := newDispatcher(t, cat, led, &attemptRecorder{}, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec)["code"] != apierr.CodeSettlementUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSuccessCompletesIdempotencyWithBody(t *testing.T) {
	up := fixedUpstream(http.StatusOK, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	idem := &fakeIdem{}
	d := newDispatcher(t, cat, &fakeLedger{}, &attemptRecorder{}, idem)

	c := testCtx(false)
	c.Idem = &gw.IdemState{Record: idemstore.Record{RequestID: c.RequestID}}
	rec := serve(t, d, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(idem.completes) != 1 || idem.completes[0] != idemstore.StateDone || len(idem.bodies[0]) == 0 {
		t.Fatalf("completes=%v bodies=%d", idem.completes, len(idem.bodies))
	}
	if !c.IdemDone {
		t.Fatal("Ctx.IdemDone must be set so the release path does not mark FAILED")
	}
}

func TestMissingRetryMaxOrModelMapIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gw.ChannelEntry)
	}{
		{"retry_max", func(ch *gw.ChannelEntry) { ch.RetryMax = nil }},
		{"upstream_model_name_map", func(ch *gw.ChannelEntry) { ch.UpstreamModelNameMap = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, envelope := testBox(t)
			cat := testCatalog(t, "http://127.0.0.1:1/v1", envelope, tc.mutate)
			led := &fakeLedger{}
			d := newDispatcher(t, cat, led, &attemptRecorder{}, &fakeIdem{})
			rec := serve(t, d, testCtx(false))
			if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec)["code"] != apierr.CodeConfigUnavailable {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if led.settleCount() != 1 || led.lastSettle().ConsumedMicro != 0 {
				t.Fatalf("hold not released: %+v", led.settles)
			}
		})
	}
}
