package httpsrv

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// Middleware is the constructor shape used by Chain.
type Middleware func(http.Handler) http.Handler

// Chain wraps h with middleware; the first element is the outermost layer.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RequestID accepts a client-supplied X-Request-Id or generates a UUIDv7.
func RequestID(c clock.Clock) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := sanitizeHeaderValue(r.Header.Get("X-Request-Id"), 128)
			if rid == "" {
				rid = newUUIDv7(c.Now())
			}
			meta := obslog.MetaFrom(r.Context())
			meta.RequestID = rid
			w.Header().Set("X-Request-Id", rid)
			next.ServeHTTP(w, r.WithContext(obslog.WithMeta(r.Context(), meta)))
		})
	}
}

// TraceID resolves a W3C traceparent trace-id, an X-Trace-Id, or derives a
// 32-hex trace id from the request id so logs remain correlated.
func TraceID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			meta := obslog.MetaFrom(r.Context())
			tid := traceIDFromHeaders(r.Header)
			if tid == "" {
				tid = traceIDFromRequestID(meta.RequestID)
			}
			meta.TraceID = tid
			w.Header().Set("X-Trace-Id", tid)
			next.ServeHTTP(w, r.WithContext(obslog.WithMeta(r.Context(), meta)))
		})
	}
}

// Recover converts a panic into a JSON 500 without leaking the stack or panic
// value to the client.
func Recover(l *obslog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				l.Error(r.Context(), fmt.Sprintf("panic recovered: %v", rec), obslog.Fields{
					Stage:  "recover",
					Status: http.StatusInternalServerError,
				})
				if !sw.wroteHeader {
					writeJSON(sw, http.StatusInternalServerError, map[string]string{"status": "error"})
				}
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// AccessLog emits one fixed-schema line per request.
func AccessLog(l *obslog.Logger, c clock.Clock) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := c.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			l.Info(r.Context(), "http request", obslog.Fields{
				Stage:     "http",
				LatencyMS: c.Now().Sub(start).Milliseconds(),
				Status:    sw.status,
			})
		})
	}
}

// statusWriter records the status code and preserves http.Flusher for SSE.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	b, err := jsonx.Marshal(body)
	if err != nil {
		http.Error(w, `{"status":"error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// newUUIDv7 generates a UUIDv7 string. The 48-bit timestamp comes from the
// injectable clock; the remaining bits are random.
func newUUIDv7(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand failures are effectively fatal; retain monotonic-ish
		// uniqueness from the clock and a per-call nanosecond value.
		ns := uint64(now.UnixNano())
		for i := 6; i < 16; i++ {
			b[i] = byte(ns >> (8 * uint(i-6)))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func traceIDFromHeaders(h http.Header) string {
	if tp := strings.TrimSpace(h.Get("traceparent")); tp != "" {
		parts := strings.Split(tp, "-")
		if len(parts) == 4 && parts[0] == "00" && isLowerHex(parts[1], 32) && !allZero(parts[1]) {
			return parts[1]
		}
	}
	if v := strings.ToLower(strings.TrimSpace(h.Get("X-Trace-Id"))); isLowerHex(v, 32) && !allZero(v) {
		return v
	}
	return ""
}

func traceIDFromRequestID(rid string) string {
	compact := strings.ToLower(strings.ReplaceAll(rid, "-", ""))
	if isLowerHex(compact, 32) {
		return compact
	}
	sum := sha256.Sum256([]byte(rid))
	return hex.EncodeToString(sum[:16])
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func allZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

func sanitizeHeaderValue(v string, max int) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
