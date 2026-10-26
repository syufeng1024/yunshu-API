package mw

import (
	"fmt"
	"net/http"

	"yunshu.dev/core/internal/apierr"
	obslog "yunshu.dev/core/internal/obs/log"
)

// Recover converts a panic into a 500 internal_error body. The stack and the
// panic value go to the structured log only; neither is echoed to the client.
//
// It is registered inside the httpsrv chain so it runs before the generic
// server-level recover: the client sees the same error envelope as every other
// control-plane failure.
func Recover(logger *obslog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &recoverWriter{ResponseWriter: w}
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if logger != nil {
					// The panic value goes to the log, never to the response.
					logger.Error(r.Context(), fmt.Sprintf("control_panic_recovered: %v", rec), obslog.Fields{
						Stage:  "recover",
						Status: http.StatusInternalServerError,
					})
				}
				if !sw.wroteHeader {
					WriteError(sw, r, apierr.New(apierr.CodeInternalError))
				}
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// recoverWriter records whether a handler already committed a status line, so
// the recover path never appends a second body to a partial response.
type recoverWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *recoverWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *recoverWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *recoverWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *recoverWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
