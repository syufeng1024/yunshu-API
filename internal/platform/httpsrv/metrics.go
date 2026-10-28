package httpsrv

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"yunshu.dev/core/internal/obs/metrics"
)

// MetricsHandler serves GET/HEAD /metrics from reg. When token is non-empty it
// requires "Authorization: Bearer <token>", compared in constant time.
//
// The body is the cumulative Registry.Snapshot() rendering, not the window
// deltas used by Registry.Run: a Prometheus counter must not reset on every
// scrape (TDS §5.4).
func MetricsHandler(token string, reg *metrics.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if token != "" {
			const prefix = "Bearer "
			got := r.Header.Get("Authorization")
			if !strings.HasPrefix(got, prefix) || !secureEqual(strings.TrimPrefix(got, prefix), token) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		if reg == nil {
			_, _ = w.Write([]byte("# yunshu metrics: registry unavailable\n"))
			return
		}
		_, _ = w.Write(reg.PrometheusText())
	})
}

func secureEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
