package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"yunshu.dev/core/internal/jsonx"
)

func TestHealthzNeverRunsDependencyChecks(t *testing.T) {
	r := New()
	var calls atomic.Int64
	r.Add("redis", func(context.Context) error {
		calls.Add(1)
		return errors.New("connection refused")
	})
	r.SetReady(true)

	rec := httptest.NewRecorder()
	r.Healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls.Load() != 0 {
		t.Fatalf("healthz executed %d dependency checks; it must execute none", calls.Load())
	}
	var body map[string]string
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v", body)
	}
}

func TestReadyzReportsDependencyDown(t *testing.T) {
	r := New()
	r.SetReady(true)
	r.Add("redis", func(context.Context) error { return errors.New("connection refused") })
	r.Add("mysql", func(context.Context) error { return nil })
	r.Add("config", func(context.Context) error { return nil })

	rec := httptest.NewRecorder()
	r.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Status != "not_ready" {
		t.Errorf("status = %q, want not_ready", body.Status)
	}
	if body.Checks["redis"] != "down" {
		t.Errorf("checks.redis = %q, want down", body.Checks["redis"])
	}
	if body.Checks["mysql"] != "ok" || body.Checks["config"] != "ok" {
		t.Errorf("checks = %v", body.Checks)
	}
}

func TestReadyzIs503WhileDraining(t *testing.T) {
	r := New()
	r.Add("redis", func(context.Context) error { return nil })
	r.SetReady(false)

	rec := httptest.NewRecorder()
	r.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Fatalf("body = %v", body)
	}
}
