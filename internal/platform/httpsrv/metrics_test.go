package httpsrv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	obsmetrics "yunshu.dev/core/internal/obs/metrics"
)

func TestMetricsHandlerServesRegistryWithBearerAuth(t *testing.T) {
	reg := obsmetrics.NewRegistry()
	_ = obsmetrics.NewRecorder(reg)
	h := MetricsHandler("s3cret", reg)

	t.Run("missing token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer nope")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("authorized GET", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "version=0.0.4") {
			t.Fatalf("content type = %q", got)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "# TYPE gw_requests_total counter") {
			t.Fatalf("body missing the registry families:\n%s", body)
		}
	})

	t.Run("HEAD has no body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
		}
	})

	t.Run("POST is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow = %q", got)
		}
	})
}

func TestMetricsHandlerWithoutTokenNeedsNoAuth(t *testing.T) {
	reg := obsmetrics.NewRegistry()
	h := MetricsHandler("", reg)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
