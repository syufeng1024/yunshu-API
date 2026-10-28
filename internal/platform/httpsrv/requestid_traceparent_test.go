package httpsrv

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// TestRequestIDTraceparentEndToEnd drives the real middleware chain rather than
// RequestID and TraceID in isolation: RequestID must finish before TraceID, or
// a traceparent-only request ends up with three different identifiers (response
// request id, response trace id, log trace_id).
func TestRequestIDTraceparentEndToEnd(t *testing.T) {
	const (
		traceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
		traceparent = "00-" + traceID + "-00f067aa0ba902b7-01"
	)
	uuidV7Shape := regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	traceHexShape := regexp.MustCompile(`^[0-9a-f]{32}$`)

	run := func(t *testing.T, req *http.Request) (http.Header, obslog.Meta) {
		t.Helper()
		var seen obslog.Meta
		h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// AccessLog renders trace_id/request_id from this Meta, so asserting
			// it asserts the log fields too.
			seen = obslog.MetaFrom(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}), RequestID(clock.Real{}), TraceID())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header(), seen
	}

	t.Run("traceparent_only", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
		req.Header.Set("traceparent", traceparent)
		hdr, meta := run(t, req)

		if got := hdr.Get("X-Request-Id"); got != traceID {
			t.Fatalf("X-Request-Id = %q, want trace-id %q", got, traceID)
		}
		if got := hdr.Get("X-Trace-Id"); got != traceID {
			t.Fatalf("X-Trace-Id = %q, want %q", got, traceID)
		}
		if meta.RequestID != traceID || meta.TraceID != traceID {
			t.Fatalf("handler meta = %+v, want request/trace id %q", meta, traceID)
		}
	})

	t.Run("client_rid_plus_traceparent", func(t *testing.T) {
		const clientRID = "client-rid-1"
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
		req.Header.Set("X-Request-Id", clientRID)
		req.Header.Set("traceparent", traceparent)
		hdr, meta := run(t, req)

		if got := hdr.Get("X-Request-Id"); got != clientRID {
			t.Fatalf("X-Request-Id = %q, want client value %q", got, clientRID)
		}
		if got := hdr.Get("X-Trace-Id"); got != traceID {
			t.Fatalf("X-Trace-Id = %q, want trace-id %q", got, traceID)
		}
		if meta.RequestID != clientRID || meta.TraceID != traceID {
			t.Fatalf("handler meta = %+v", meta)
		}
	})

	t.Run("neither", func(t *testing.T) {
		// Known legacy divergence: the fallback X-Request-Id is a hyphenated
		// UUIDv7 while X-Trace-Id is the same id reduced to 32 hex chars, so the
		// two cannot be equal here. Equalling them would mean changing
		// apierr.NewRequestID's UUIDv7 shape, which would affect idempotency
		// ordering, the audit trail and the width of t_usage_record.request_id.
		// The card only requires equality when a traceparent is present.
		req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
		hdr, meta := run(t, req)

		rid := hdr.Get("X-Request-Id")
		tid := hdr.Get("X-Trace-Id")
		if !uuidV7Shape.MatchString(rid) {
			t.Fatalf("X-Request-Id = %q, want UUIDv7 shape", rid)
		}
		if !traceHexShape.MatchString(tid) {
			t.Fatalf("X-Trace-Id = %q, want 32 lowercase hex chars", tid)
		}
		if rid == "" || tid == "" {
			t.Fatalf("both ids must be non-empty: rid=%q tid=%q", rid, tid)
		}
		if rid == tid {
			t.Fatalf("rid and trace id unexpectedly equal: %q", rid)
		}
		if meta.RequestID != rid || meta.TraceID != tid {
			t.Fatalf("handler meta = %+v, header rid=%q tid=%q", meta, rid, tid)
		}
	})
}
