package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	log "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/redisx"
)

//go:embed scripts/rl_release.lua
var releaseScriptSource string

var releaseScript = goredis.NewScript(releaseScriptSource)

// ReleaseInput is one exit-path release. TPMDelta is the value returned by
// SettleAfterRelease; DailyDelta is -hold on release or consumed-hold on
// settle. Both may be negative.
type ReleaseInput struct {
	KeyID  int64
	UserID int64

	RequestID       string
	AcquireMinEpoch int64
	CurrentMinEpoch int64
	Day             string

	TPMDelta   int64
	DailyDelta int64

	// HadLease is true when Acquire used K3. In concurrency-degraded mode it
	// is false: the release only compensates TPM/daily and never touches K3.
	HadLease bool
}

// ReleaseResult reports the remaining concurrency leases.
type ReleaseResult struct {
	Leases int64
}

// Release is safe on every exit path: success, failure, client disconnect and
// the panic-recovery defer. A Redis failure is not fail-close here — the
// ledger has already settled and the release only corrects counter precision.
// Such releases go into a bounded buffer for ReplayPending.
func (l *Limiter) Release(ctx context.Context, in ReleaseInput) (ReleaseResult, error) {
	if !in.HadLease && l.degrader != nil && l.degrader.Enabled() {
		// The local counter is independent of Redis: return the slot even if
		// the TPM/daily compensation below has to be replayed.
		l.degrader.ReleaseLocal(in.KeyID, in.RequestID)
	}
	result, err := l.tryRelease(ctx, in)
	if err != nil {
		l.pending.push(in)
		if l.logger != nil {
			l.logger.Warn(ctx, "rate_limit_release_queued: "+err.Error(), log.Fields{Stage: "ratelimit"})
		}
		return ReleaseResult{}, err
	}
	return result, nil
}

// ReplayPending retries buffered releases. It returns how many succeeded.
// Failures stay queued for the next tick (5s in the gateway).
func (l *Limiter) ReplayPending(ctx context.Context) int {
	items := l.pending.drain()
	ok := 0
	for _, in := range items {
		if _, err := l.tryRelease(ctx, in); err != nil {
			l.pending.push(in)
			continue
		}
		ok++
	}
	return ok
}

// PendingReleases reports the buffer depth so the gateway can expose it.
func (l *Limiter) PendingReleases() int { return l.pending.len() }

func (l *Limiter) tryRelease(ctx context.Context, in ReleaseInput) (ReleaseResult, error) {
	if in.RequestID == "" {
		return ReleaseResult{}, fmt.Errorf("ratelimit: empty request_id")
	}
	if in.CurrentMinEpoch == 0 {
		in.CurrentMinEpoch = redisx.MinEpochFromMS(l.clock.NowMS())
	}
	if in.AcquireMinEpoch == 0 {
		in.AcquireMinEpoch = in.CurrentMinEpoch
	}
	day := in.Day
	if day == "" {
		day = Day(time.UnixMilli(l.clock.NowMS()))
	}

	requireMember := int64(0)
	if in.HadLease {
		requireMember = 1
	}

	keys := []string{
		redisx.ConcurrencyKey(in.KeyID),
		redisx.TPMKey(in.KeyID, in.AcquireMinEpoch),
		redisx.DailyQuotaKey(in.UserID, day),
	}
	args := []interface{}{
		in.RequestID,
		in.TPMDelta,
		in.DailyDelta,
		in.AcquireMinEpoch,
		in.CurrentMinEpoch,
		requireMember,
	}

	lctx, cancel := l.client.LuaContext(ctx)
	defer cancel()
	raw, err := releaseScript.Run(lctx, l.client.Raw(), keys, args...).Result()
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("release: %w", err)
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) != 2 {
		return ReleaseResult{}, fmt.Errorf("release: unexpected reply %T", raw)
	}
	return ReleaseResult{Leases: asInt64(values[1])}, nil
}

type pendingQueue struct {
	mu    sync.Mutex
	items []ReleaseInput
	max   int
}

func newPendingQueue(max int) *pendingQueue {
	return &pendingQueue{items: make([]ReleaseInput, 0, 64), max: max}
}

func (q *pendingQueue) push(in ReleaseInput) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.max {
		// Drop the oldest: a release that has waited through the whole buffer
		// is already likely to be superseded by the daily reconcile.
		copy(q.items, q.items[1:])
		q.items[len(q.items)-1] = in
		return
	}
	q.items = append(q.items, in)
}

func (q *pendingQueue) drain() []ReleaseInput {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = make([]ReleaseInput, 0, 64)
	return out
}

func (q *pendingQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
