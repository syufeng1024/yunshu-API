package settle

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

// BacklogThreshold is the GR-03 backpressure line. Above it the gateway rejects
// NEW requests with 503 backpressure_rejected; requests already in flight are
// never rejected (T09 hard constraint 5).
const BacklogThreshold = 5000

// Backlog counts PENDING settlement tasks.
//
// Counting on every request would put a SELECT COUNT(*) on the gateway's hot
// path. The value is cached for one second instead: at most one query per
// second per gateway process, and a burst of 5000 tasks is still visible within
// the second. A database error returns the last known value (fail-open for the
// capacity gate — refusing traffic because a COUNT failed would turn a DB
// hiccup into a total outage).
type Backlog struct {
	DB    *sql.DB
	Clock clock.Clock
	TTL   time.Duration

	mu      sync.Mutex
	value   int64
	checked time.Time
}

// NewBacklog builds a one-second-cached backlog counter.
func NewBacklog(db *sql.DB, clk clock.Clock) *Backlog {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Backlog{DB: db, Clock: clk, TTL: time.Second}
}

// SettleBacklog returns the number of PENDING tasks, cached for TTL.
func (b *Backlog) SettleBacklog(ctx context.Context) (int64, error) {
	if b == nil {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if !b.checked.IsZero() && now.Sub(b.checked) < b.ttl() {
		return b.value, nil
	}
	if b.DB == nil {
		return b.value, nil
	}
	var n int64
	if err := b.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_settle_task WHERE state = ?`, TaskStatePending,
	).Scan(&n); err != nil {
		// Keep the stale value rather than reporting zero: a zero would
		// silently disable backpressure.
		return b.value, err
	}
	b.value = n
	b.checked = now
	return n, nil
}

// Exceeded reports whether the backlog is above BacklogThreshold.
func (b *Backlog) Exceeded(ctx context.Context) bool {
	n, err := b.SettleBacklog(ctx)
	return err == nil && n > BacklogThreshold
}

// Invalidate drops the cached value. Tests call it after fixtures change.
func (b *Backlog) Invalidate() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.checked = time.Time{}
	b.mu.Unlock()
}

func (b *Backlog) ttl() time.Duration {
	if b.TTL <= 0 {
		return time.Second
	}
	return b.TTL
}

func (b *Backlog) now() time.Time {
	if b.Clock == nil {
		return time.Now().UTC()
	}
	return b.Clock.Now().UTC()
}
