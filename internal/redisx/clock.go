package redisx

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
	pclock "yunshu.dev/core/internal/platform/clock"
)

// Offset thresholds from TDS §2.2. Two replicas that disagree about the
// current minute write two different K1/K2 buckets, so each of them gets the
// full allowance — a clock skew of one minute doubles the effective limit.
const (
	clockWarnOffsetMS  = int64(1_000)
	clockErrorOffsetMS = int64(5_000)
)

// OffsetObserver receives every sampled offset, including the fine ones, so
// the exporter can render gw_* clock skew gauges. Implementations must be
// non-blocking: Sync runs on the rate-limiter hot path's timer.
type OffsetObserver interface {
	ObserveClockOffset(offsetMS int64)
}

// Logger is the subset of obs/log used here. A nil logger is allowed.
type Logger interface {
	Warn(ctx context.Context, msg string, f obslog.Fields)
	Error(ctx context.Context, msg string, f obslog.Fields)
}

// Clock stores the correction between the local clock and Redis TIME.
//
// MinEpoch must always be computed from NowMS, never from time.Now directly:
// the local clock is only an input to the corrected timestamp.
type Clock struct {
	now      func() time.Time
	offsetMS atomic.Int64
	logger   Logger
	observer OffsetObserver
}

// NewClock returns a clock backed by the process clock.
func NewClock(logger Logger, observer OffsetObserver) *Clock {
	real := pclock.Real{}
	return &Clock{now: real.Now, logger: logger, observer: observer}
}

// NewStaticClock freezes the local clock. Tests use it to place a request on
// either side of a minute or a CST day boundary.
func NewStaticClock(t time.Time) *Clock {
	utc := t.UTC()
	return &Clock{now: func() time.Time { return utc }}
}

// NewAdjustableClock returns a clock whose local time a test can move. The
// returned setter is safe for a single goroutine; production code must use
// NewClock.
func NewAdjustableClock(t time.Time) (*Clock, func(time.Time)) {
	var nanos atomic.Int64
	nanos.Store(t.UTC().UnixNano())
	return &Clock{now: func() time.Time { return time.Unix(0, nanos.Load()).UTC() }},
		func(next time.Time) { nanos.Store(next.UTC().UnixNano()) }
}

// NowMS returns local time corrected by the most recent TIME sample.
func (c *Clock) NowMS() int64 { return c.now().UnixMilli() + c.offsetMS.Load() }

// MinEpoch returns the corrected per-minute bucket ordinal for K1/K2.
func (c *Clock) MinEpoch() int64 { return MinEpochFromMS(c.NowMS()) }

// Now returns the corrected wall clock.
func (c *Clock) Now() time.Time { return time.UnixMilli(c.NowMS()) }

// OffsetMS returns the last sampled correction.
func (c *Clock) OffsetMS() int64 { return c.offsetMS.Load() }

// Sync samples Redis TIME once and stores the midpoint-corrected offset.
// Returns the new offset; an error means the sample was not applied.
func (c *Clock) Sync(ctx context.Context, client *Client) (int64, error) {
	t0 := c.now().UnixMilli()
	lctx, cancel := client.LuaContext(ctx)
	defer cancel()
	values, err := client.Raw().Do(lctx, "TIME").StringSlice()
	if err != nil {
		return c.offsetMS.Load(), fmt.Errorf("redisx: TIME: %w", err)
	}
	t1 := c.now().UnixMilli()
	if len(values) != 2 {
		return c.offsetMS.Load(), fmt.Errorf("redisx: TIME returned %d fields", len(values))
	}
	sec, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return c.offsetMS.Load(), fmt.Errorf("redisx: TIME seconds: %w", err)
	}
	usec, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return c.offsetMS.Load(), fmt.Errorf("redisx: TIME microseconds: %w", err)
	}
	remoteMS := sec*1000 + usec/1000
	return c.applySample(ctx, remoteMS, t0, t1), nil
}

// applySample stores and reports one offset. It is separate from Sync so the
// threshold behaviour can be tested without a skewed server.
func (c *Clock) applySample(ctx context.Context, remoteMS, t0, t1 int64) int64 {
	offset := remoteMS - (t0+t1)/2
	c.offsetMS.Store(offset)

	abs := offset
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs > clockErrorOffsetMS:
		if c.logger != nil {
			c.logger.Error(ctx, fmt.Sprintf("redis_clock_skew: offset_ms=%d", offset),
				obslog.Fields{Stage: "ratelimit"})
		}
	case abs > clockWarnOffsetMS:
		if c.logger != nil {
			c.logger.Warn(ctx, fmt.Sprintf("redis_clock_skew: offset_ms=%d", offset),
				obslog.Fields{Stage: "ratelimit"})
		}
	}
	if c.observer != nil {
		c.observer.ObserveClockOffset(offset)
	}
	return offset
}

// StartSync samples TIME immediately and then every interval (one minute by
// default). The returned function stops the loop and waits for it to exit.
func (c *Clock) StartSync(ctx context.Context, client *Client, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Sync(runCtx, client)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_, _ = c.Sync(runCtx, client)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
