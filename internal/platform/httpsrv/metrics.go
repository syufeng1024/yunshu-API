package httpsrv

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// metricsBody is a valid Prometheus text exposition with no series yet. Metric
// registration lands with the gateway pipeline; T01 only proves the isolated
// listener and its authentication path work.
const metricsBody = "# yunshu metrics: no series registered in T01\n"

// MetricsHandler serves GET/HEAD /metrics. When token is non-empty it requires
// "Authorization: Bearer <token>", compared in constant time.
func MetricsHandler(token string) http.Handler {
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
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(metricsBody))
		}
	})
}

func secureEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
