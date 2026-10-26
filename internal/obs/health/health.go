// Package health implements the liveness/readiness split.
//
// /healthz answers only "is this process alive" and never touches a dependency.
// /readyz runs the registered dependency checks and returns 503 when any check
// fails, with the per-dependency result in the body.
package health

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// DefaultCheckTimeout bounds one dependency check. Redis PING uses the same
// budget per the observability spec.
const DefaultCheckTimeout = 200 * time.Millisecond

// Check is a named readiness probe.
type Check struct {
	Name string
	Fn   func(context.Context) error
}

// Registry holds readiness checks and the current ready flag.
type Registry struct {
	ready   atomic.Bool
	timeout time.Duration
	mu      sync.RWMutex
	checks  []Check
}

// New returns an empty registry. A process is not ready until SetReady(true).
func New() *Registry {
	return &Registry{timeout: DefaultCheckTimeout}
}

// Add registers a readiness check.
func (r *Registry) Add(name string, fn func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, Check{Name: name, Fn: fn})
}

// SetReady flips the readiness gate. It is set to false as soon as a drain
// starts so the load balancer stops sending new requests.
func (r *Registry) SetReady(v bool) {
	r.ready.Store(v)
}

// Ready reports the current gate value.
func (r *Registry) Ready() bool {
	return r.ready.Load()
}

// Healthz reports process liveness only. It must never inspect r.checks.
func (r *Registry) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz runs every registered check concurrently and reports the aggregate.
func (r *Registry) Readyz(w http.ResponseWriter, req *http.Request) {
	if !r.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready",
			"checks": map[string]string{},
		})
		return
	}

	r.mu.RLock()
	checks := append([]Check(nil), r.checks...)
	r.mu.RUnlock()

	results := make(map[string]string, len(checks))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
		ok = true
	)
	for _, c := range checks {
		wg.Add(1)
		go func(c Check) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(req.Context(), r.timeout)
			defer cancel()

			err := c.Fn(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results[c.Name] = "down"
				ok = false
				return
			}
			results[c.Name] = "ok"
		}(c)
	}
	wg.Wait()

	status := http.StatusOK
	label := "ready"
	if !ok {
		status = http.StatusServiceUnavailable
		label = "not_ready"
	}
	writeJSON(w, status, map[string]any{
		"status": label,
		"checks": results,
	})
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
