package jobs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// TaskFunc is one task body. scheduled is the nominal fire time in the
// business timezone.
type TaskFunc func(ctx context.Context, scheduled time.Time) error

// Runner executes registered tasks. Every task with RequiresLeader runs only
// while its t_job_lock lease is held and renewed; a second jobsd skips the tick
// instead of running a duplicate (hard constraint 15).
type Runner struct {
	Registry  *Registry
	Election  *Election
	Logger    *obslog.Logger
	Clock     clock.Clock
	Location  *time.Location
	Executors map[string]TaskFunc
}

// Validate checks that every registered task has a body and a parseable cron.
func (r *Runner) Validate() error {
	if r == nil || r.Registry == nil {
		return fmt.Errorf("jobs: runner requires a registry")
	}
	if r.Location == nil {
		r.Location = time.FixedZone("CST", 8*3600)
	}
	for _, task := range r.Registry.List() {
		if _, ok := r.Executors[task.Name]; !ok {
			return fmt.Errorf("jobs: task %s has no executor", task.Name)
		}
		if task.Cron != "" {
			if _, err := parseCron(task.Cron); err != nil {
				return fmt.Errorf("jobs: task %s: %w", task.Name, err)
			}
		}
	}
	return nil
}

// Start launches one goroutine per task and blocks until ctx is cancelled.
func (r *Runner) Start(ctx context.Context) error {
	if err := r.Validate(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, task := range r.Registry.List() {
		fn := r.Executors[task.Name]
		wg.Add(1)
		go func(task Task, fn TaskFunc) {
			defer wg.Done()
			if task.RequiresLeader {
				if r.Election == nil {
					r.log(ctx, obslog.LevelError, "job_skipped_no_election job="+task.Name)
					return
				}
				r.leaderLoop(ctx, task, fn)
				return
			}
			r.readLoop(ctx, task, fn)
		}(task, fn)
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (r *Runner) readLoop(ctx context.Context, task Task, fn TaskFunc) {
	for {
		next := r.nextRun(task, r.now())
		if !r.wait(ctx, next) {
			return
		}
		r.execute(ctx, task, fn, next)
	}
}

func (r *Runner) leaderLoop(ctx context.Context, task Task, fn TaskFunc) {
	owned := false
	busyLogged := false
	renewEvery := r.Election.renewEvery()
	renewTicker := time.NewTicker(renewEvery)
	defer renewTicker.Stop()

	// Try immediately: a process that starts seconds before a cron boundary
	// must not miss that boundary while waiting for the first renew tick.
	if ok, err := r.Election.Acquire(ctx, task.Name); err != nil {
		r.log(ctx, obslog.LevelWarn, "job_lock_acquire_failed job="+task.Name+": "+err.Error())
	} else if ok {
		owned = true
		r.log(ctx, obslog.LevelInfo, "job_lock_acquired job="+task.Name)
	}

	// next is fixed across renew ticks. Recomputing it on every renewal would
	// push a 60s period task forward by 60s every 20s and it would never fire.
	next := r.nextRun(task, r.now())
	for {
		wait := next.Sub(r.now())
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			r.release(ctx, task.Name, owned)
			return
		case <-renewTicker.C:
			timer.Stop()
			if owned {
				ok, err := r.Election.Renew(ctx, task.Name)
				if err != nil {
					r.log(ctx, obslog.LevelWarn, "job_lock_renew_failed job="+task.Name+": "+err.Error())
				}
				if !ok {
					owned = false
					r.log(ctx, obslog.LevelWarn, "job_lock_lost job="+task.Name)
				}
				continue
			}
			ok, err := r.Election.Acquire(ctx, task.Name)
			if err != nil {
				r.log(ctx, obslog.LevelWarn, "job_lock_acquire_failed job="+task.Name+": "+err.Error())
				continue
			}
			if ok {
				owned = true
				busyLogged = false
				r.log(ctx, obslog.LevelInfo, "job_lock_acquired job="+task.Name)
			} else if !busyLogged {
				busyLogged = true
				owner, leaseUntil, _, _ := r.Election.Holder(ctx, task.Name)
				r.log(ctx, obslog.LevelInfo, fmt.Sprintf(
					"job_lock_held_by_other job=%s owner=%s lease_until=%s",
					task.Name, owner, leaseUntil.UTC().Format(time.RFC3339)))
			}
		case <-timer.C:
			if owned {
				r.execute(ctx, task, fn, next)
			} else {
				r.log(ctx, obslog.LevelInfo, "job_skipped_not_leader job="+task.Name)
			}
			next = r.nextRun(task, r.now())
		}
	}
}

func (r *Runner) execute(ctx context.Context, task Task, fn TaskFunc, scheduled time.Time) {
	timeout := task.Timeout
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// purge_log may run for an hour, far beyond the 90s lease. Renew from a
	// separate goroutine so the leader does not lose the lock mid-task and a
	// second jobsd starts the same side-effecting job.
	if task.RequiresLeader && r.Election != nil {
		stopRenew := make(chan struct{})
		go func() {
			ticker := time.NewTicker(r.Election.renewEvery())
			defer ticker.Stop()
			for {
				select {
				case <-stopRenew:
					return
				case <-runCtx.Done():
					return
				case <-ticker.C:
					ok, err := r.Election.Renew(runCtx, task.Name)
					if err != nil {
						r.log(ctx, obslog.LevelWarn, "job_lock_renew_failed job="+task.Name+": "+err.Error())
						continue
					}
					if !ok {
						r.log(ctx, obslog.LevelError, "job_lock_lost_during_run job="+task.Name)
						return
					}
				}
			}
		}()
		defer close(stopRenew)
	}

	start := r.now()
	err := fn(runCtx, scheduled)
	ms := r.now().Sub(start).Milliseconds()
	if err != nil {
		r.log(ctx, obslog.LevelError, fmt.Sprintf("task_failed job=%s latency_ms=%d: %v",
			task.Name, ms, err))
		return
	}
	r.log(ctx, obslog.LevelInfo, fmt.Sprintf("task_done job=%s latency_ms=%d", task.Name, ms))
}

func (r *Runner) release(ctx context.Context, job string, owned bool) {
	if !owned || r.Election == nil {
		return
	}
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := r.Election.Release(relCtx, job); err != nil {
		r.log(ctx, obslog.LevelWarn, "job_lock_release_failed job="+job+": "+err.Error())
		return
	}
	r.log(ctx, obslog.LevelInfo, "job_lock_released job="+job)
}

func (r *Runner) nextRun(task Task, now time.Time) time.Time {
	if task.Period > 0 {
		return now.Add(task.Period)
	}
	expr, err := parseCron(task.Cron)
	if err != nil {
		// Validate rejects this before Start; fail closed rather than busy-loop.
		return now.Add(24 * time.Hour)
	}
	return expr.next(now.In(r.Location))
}

func (r *Runner) wait(ctx context.Context, until time.Time) bool {
	d := until.Sub(r.now())
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *Runner) now() time.Time {
	if r == nil || r.Clock == nil {
		return time.Now().UTC()
	}
	return r.Clock.Now().UTC()
}

func (r *Runner) log(ctx context.Context, level obslog.Level, msg string) {
	if r == nil || r.Logger == nil {
		return
	}
	switch level {
	case obslog.LevelError:
		r.Logger.Error(ctx, msg, obslog.Fields{Stage: "jobs"})
	case obslog.LevelWarn:
		r.Logger.Warn(ctx, msg, obslog.Fields{Stage: "jobs"})
	default:
		r.Logger.Info(ctx, msg, obslog.Fields{Stage: "jobs"})
	}
}

// ---- minimal 6-field cron ------------------------------------------------

type cronField struct {
	star bool
	vals map[int]bool
}

type cronExpr struct {
	sec, min, hour, dom, month, dow cronField
}

func parseCron(expr string) (cronExpr, error) {
	parts := strings.Fields(expr)
	if len(parts) != 6 {
		return cronExpr{}, fmt.Errorf("cron %q needs 6 fields", expr)
	}
	fields := make([]cronField, 6)
	for i, p := range parts {
		f, err := parseField(p, i)
		if err != nil {
			return cronExpr{}, fmt.Errorf("cron %q field %d: %w", expr, i, err)
		}
		fields[i] = f
	}
	return cronExpr{fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]}, nil
}

func parseField(s string, pos int) (cronField, error) {
	if s == "*" {
		return cronField{star: true}, nil
	}
	lo, hi := 0, 59
	switch pos {
	case 2:
		hi = 23
	case 3:
		lo, hi = 1, 31
	case 4:
		lo, hi = 1, 12
	case 5:
		lo, hi = 0, 6
	}
	f := cronField{vals: map[int]bool{}}
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(part)
		if err != nil || n < lo || n > hi {
			return cronField{}, fmt.Errorf("unsupported value %q", part)
		}
		f.vals[n] = true
	}
	return f, nil
}

func (f cronField) match(v int) bool { return f.star || f.vals[v] }

func (c cronExpr) matches(t time.Time) bool {
	return c.sec.match(t.Second()) && c.min.match(t.Minute()) &&
		c.hour.match(t.Hour()) && c.dom.match(t.Day()) &&
		c.month.match(int(t.Month())) && c.dow.match(int(t.Weekday()))
}

func (f cronField) first() int {
	if f.star {
		return 0
	}
	best := -1
	for v := range f.vals {
		if best == -1 || v < best {
			best = v
		}
	}
	if best == -1 {
		return 0
	}
	return best
}

// next returns the first matching instant strictly after t. It scans minutes
// (skipping the seconds field) and is bounded to 400 days.
func (c cronExpr) next(t time.Time) time.Time {
	sec := c.sec.first()
	loc := t.Location()
	cursor := t.Truncate(time.Minute).Add(time.Minute)
	limit := cursor.AddDate(1, 1, 0)
	for cursor.Before(limit) {
		if c.min.match(cursor.Minute()) && c.hour.match(cursor.Hour()) &&
			c.dom.match(cursor.Day()) && c.month.match(int(cursor.Month())) &&
			c.dow.match(int(cursor.Weekday())) {
			return time.Date(cursor.Year(), cursor.Month(), cursor.Day(),
				cursor.Hour(), cursor.Minute(), sec, 0, loc)
		}
		cursor = cursor.Add(time.Minute)
	}
	return t.Add(24 * time.Hour)
}
