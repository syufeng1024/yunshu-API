// Package jobs is the background-task runtime shared by jobsd and the T09
// reconciliation tasks.
//
// The registry is metadata only: name, schedule, leader requirement, per-run
// timeout and concurrency policy. Task bodies arrive in T09's second segment.
// Every task that writes business data MUST set RequiresLeader; a task that
// reads only may run without the lease (T09 hard constraint 15).
package jobs

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Concurrency policies. SERIAL is the only policy V1.0 implements; a second
// policy must be added deliberately, not inferred.
const (
	ConcurrencySerial = "SERIAL"
)

// Task is one registered job.
type Task struct {
	Name string
	// Cron is the schedule expression when Period is zero (e.g. "0 5 0 * * *").
	Cron string
	// Period is the interval schedule. Exactly one of Period / Cron is set.
	Period time.Duration
	// RequiresLeader must be false only for a pure-read task.
	RequiresLeader bool
	// Timeout bounds one execution. Zero means DefaultTaskTimeout.
	Timeout time.Duration
	// Concurrency is SERIAL by default.
	Concurrency string
}

// DefaultTaskTimeout bounds a task that did not declare one.
const DefaultTaskTimeout = 5 * time.Minute

// Registry stores tasks by name.
type Registry struct {
	mu    sync.RWMutex
	order []string
	tasks map[string]Task
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{tasks: map[string]Task{}}
}

// Register validates and adds one task. Re-registering the same name is an
// error: two definitions of one job would let one silently shadow the other.
func (r *Registry) Register(t Task) error {
	if r == nil {
		return errors.New("jobs: nil registry")
	}
	if t.Name == "" {
		return errors.New("jobs: task name required")
	}
	if t.Period <= 0 && t.Cron == "" {
		return fmt.Errorf("jobs: task %s needs a period or cron", t.Name)
	}
	if t.Period > 0 && t.Cron != "" {
		return fmt.Errorf("jobs: task %s must use period or cron, not both", t.Name)
	}
	if t.Timeout < 0 {
		return fmt.Errorf("jobs: task %s timeout must be positive", t.Name)
	}
	if t.Timeout == 0 {
		t.Timeout = DefaultTaskTimeout
	}
	if t.Concurrency == "" {
		t.Concurrency = ConcurrencySerial
	}
	if t.Concurrency != ConcurrencySerial {
		return fmt.Errorf("jobs: task %s unknown concurrency %q", t.Name, t.Concurrency)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[t.Name]; exists {
		return fmt.Errorf("jobs: task %s already registered", t.Name)
	}
	r.tasks[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// List returns the tasks in registration order.
func (r *Registry) List() []Task {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Task, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tasks[name])
	}
	return out
}

// Names returns the registered names in registration order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Sorted returns the tasks sorted by name, for deterministic logging.
func (r *Registry) Sorted() []Task {
	out := r.List()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns one task.
func (r *Registry) Lookup(name string) (Task, bool) {
	if r == nil {
		return Task{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[name]
	return t, ok
}

// Len returns the number of registered tasks.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}
