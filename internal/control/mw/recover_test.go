package mw

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverWritesInternalErrorWithoutStack(t *testing.T) {
	h := Recover(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: secret-panic-value")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/console/models", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"internal_error"`) {
		t.Fatalf("body = %s", body)
	}
	if strings.Contains(body, "secret-panic-value") || strings.Contains(body, "goroutine") {
		t.Fatalf("panic detail leaked: %s", body)
	}
}
