package httpsrv

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

func TestRequestAndTraceIDPropagation(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	seenCh := make(chan obslog.Meta, 1)
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCh <- obslog.MetaFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}), RequestID(clock.Real{}), TraceID())

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "client-request-1")
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := resp.Header.Get("X-Request-Id"); got != "client-request-1" {
		t.Errorf("X-Request-Id = %q", got)
	}
	if got := resp.Header.Get("X-Trace-Id"); got != traceID {
		t.Errorf("X-Trace-Id = %q, want %q", got, traceID)
	}
	seen := <-seenCh
	if seen.RequestID != "client-request-1" || seen.TraceID != traceID {
		t.Errorf("handler meta = %+v", seen)
	}
}

func TestRequestIDGeneratesUUIDv7(t *testing.T) {
	seenCh := make(chan obslog.Meta, 1)
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCh <- obslog.MetaFrom(r.Context())
	}), RequestID(clock.Real{}), TraceID())

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	seen := <-seenCh
	parts := strings.Split(seen.RequestID, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || parts[2][0] != '7' {
		t.Fatalf("request id %q is not UUIDv7-shaped", seen.RequestID)
	}
	if len(seen.TraceID) != 32 {
		t.Fatalf("trace id %q is not 32 hex chars", seen.TraceID)
	}
}

func TestRecoverReturnsJSON500(t *testing.T) {
	var buf strings.Builder
	l := obslog.NewWithWriter("controld", "test", &buf, clock.Real{})
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), RequestID(clock.Real{}), TraceID(), AccessLog(l, clock.Real{}), Recover(l))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Fatalf("log = %q", buf.String())
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatal("panic value leaked to the client")
	}
}
