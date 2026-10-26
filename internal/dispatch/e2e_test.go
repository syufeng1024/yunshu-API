package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunshu.dev/core/internal/fakeupstream"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ratelimit"
	"yunshu.dev/core/internal/redisx"
)

type e2eAuth struct{ rec redisx.KeyAuthRecord }

func (a e2eAuth) Lookup(context.Context, string) (redisx.KeyAuthRecord, error) {
	return a.rec, nil
}

type e2ePrices struct{ raw jsonx.RawMessage }

func (p e2ePrices) GetStandard(context.Context, int64) (jsonx.RawMessage, error) {
	return p.raw, nil
}

type e2eLimiter struct{}

func (e2eLimiter) Acquire(context.Context, ratelimit.AcquireInput) (ratelimit.Verdict, error) {
	return ratelimit.Verdict{Allowed: true, ConcurrencyMode: ratelimit.ConcurrencyRedis}, nil
}

func (e2eLimiter) Release(context.Context, ratelimit.ReleaseInput) (ratelimit.ReleaseResult, error) {
	return ratelimit.ReleaseResult{}, nil
}

func (e2eLimiter) RecordDailyRejection(context.Context, int64, string) error { return nil }

// A13: two real requests through gw.Handler + Dispatcher + fakeupstream. The
// stream runs in vendor-frame mode, so a byte copier would leak the vendor
// event; the parser must drop it.
func TestA13EndToEndAgainstFakeUpstream(t *testing.T) {
	up := httptest.NewServer(fakeupstream.New(fakeupstream.Options{
		Mode: fakeupstream.ModeVendorFrame,
	}))
	defer up.Close()

	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	deps := &gw.Deps{
		Catalog: cat,
		Auth: e2eAuth{rec: redisx.KeyAuthRecord{
			UID: 7, KID: 10, ST: "ENABLED", GRP: "standard", GP: 10000,
		}},
		Balance: fakeBalance{micro: 10_000},
		Prices: e2ePrices{raw: jsonx.RawMessage(
			`{"id":1,"model_type":"TEXT","in_price_micro_per_k":1000,"out_price_micro_per_k":2000}`)},
		Ledger:       led,
		Limiter:      e2eLimiter{},
		Tokens:       counterFunc(func(s string) int64 { return int64(len(s)) }),
		Backpressure: gw.NewBackpressure(nil),
		ConnGuard:    gw.NewConnGuard(nil),
		NodeID:       "test",
	}
	handler := gw.NewHandler(deps, d)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-e2e")
		req.Header.Set("X-Request-Id", "rid-e2e")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Non-streaming.
	rec := post(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-stream status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello from fake") {
		t.Fatalf("non-stream body = %s", rec.Body.String())
	}
	if rec.Header().Get("X-Credits-Consumed") == "" ||
		rec.Header().Get("X-Credits-Remaining") == "" {
		t.Fatalf("non-stream headers = %v", rec.Header())
	}

	// Streaming. The vendor frame must be gone; the parsed content must remain.
	streamRec := post(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body %s", streamRec.Code, streamRec.Body.String())
	}
	if streamRec.Header().Get("X-Credits-Consumed") != "" {
		t.Fatalf("stream must not expose X-Credits-Consumed: %v", streamRec.Header())
	}
	if streamRec.Header().Get("X-Credits-Preheld") != "1000" {
		t.Fatalf("stream X-Credits-Preheld = %q", streamRec.Header().Get("X-Credits-Preheld"))
	}
	body := streamRec.Body.String()
	if !strings.HasPrefix(streamRec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream content-type = %q", streamRec.Header().Get("Content-Type"))
	}
	if strings.Contains(body, "vendor-debug") || strings.Contains(body, `"debug":true`) {
		t.Fatalf("the non-standard frame was copied verbatim: %s", body)
	}
	if !strings.Contains(body, "hello") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream body is missing re-emitted frames: %s", body)
	}
	t.Logf("non-stream headers: consumed=%s preheld=%s remaining=%s",
		rec.Header().Get("X-Credits-Consumed"), rec.Header().Get("X-Credits-Preheld"),
		rec.Header().Get("X-Credits-Remaining"))
	t.Logf("stream headers: consumed=%q preheld=%s remaining=%s",
		streamRec.Header().Get("X-Credits-Consumed"), streamRec.Header().Get("X-Credits-Preheld"),
		streamRec.Header().Get("X-Credits-Remaining"))
	t.Logf("stream first frames: %s", firstLines(body, 4))
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
