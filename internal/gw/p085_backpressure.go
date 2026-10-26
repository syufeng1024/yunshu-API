package gw

import (
	"context"
	"sync/atomic"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	obslog "yunshu.dev/core/internal/obs/log"
)

// Backpressure thresholds (TDS §4.3). They are single-instance ratios, not
// cluster semantics: the denominator is gw.max_inflight for THIS process, and
// cluster capacity is value × replica count.
const (
	BackpressureWarnPercent   = int64(60)
	BackpressureRejectPercent = int64(90)
)

// DefaultMaxInflight mirrors the seeded gw.max_inflight value. It is used only
// when the configuration store is absent (unit tests); production always reads
// the snapshot.
const DefaultMaxInflight = 8000

// Backpressure is the process-level in-flight gate. It counts streaming and
// non-streaming requests together and rejects with 503 backpressure_*, never
// with 429: a 429 would tell the client to slow down its own request rate,
// which is not the problem.
type Backpressure struct {
	inflight atomic.Int64
	warned   atomic.Bool
	logger   *obslog.Logger
}

// NewBackpressure builds an empty gate.
func NewBackpressure(logger *obslog.Logger) *Backpressure {
	return &Backpressure{logger: logger}
}

// Inflight returns the current count.
func (b *Backpressure) Inflight() int64 { return b.inflight.Load() }

// InflightSlot is one unit of the process gate. Release is idempotent.
type InflightSlot struct {
	b        *Backpressure
	released atomic.Bool
}

// Release returns the slot. It must be called exactly once on every exit path.
func (s *InflightSlot) Release() {
	if s == nil || s.b == nil {
		return
	}
	if s.released.Swap(true) {
		return
	}
	s.b.inflight.Add(-1)
}

// Acquire reserves one slot.
//
//   - below 60%: admit;
//   - 60%..90%: reject streaming only (a stream holds ~15× the in-flight time
//     of a unary request, so it is the cheapest request to shed);
//   - above 90%: reject everything.
//
// The rejected slot is returned immediately; a rejection never leaks a count.
func (b *Backpressure) Acquire(ctx context.Context, maxInflight int64, stream bool) (*InflightSlot, *apierr.APIError) {
	if maxInflight <= 0 {
		maxInflight = DefaultMaxInflight
	}
	if b == nil {
		return &InflightSlot{}, nil
	}
	n := b.inflight.Add(1)
	pct := n * 100 / maxInflight

	switch {
	case pct > BackpressureRejectPercent:
		b.inflight.Add(-1)
		return nil, apierr.New(apierr.CodeBackpressureRejected)
	case stream && pct >= BackpressureWarnPercent:
		b.inflight.Add(-1)
		b.warn(ctx)
		return nil, apierr.New(apierr.CodeBackpressureStreamRejected)
	}

	if pct >= BackpressureWarnPercent {
		b.warn(ctx)
	}
	if b.warned.Load() && pct < BackpressureWarnPercent {
		b.warned.Store(false)
	}
	return &InflightSlot{b: b}, nil
}

func (b *Backpressure) warn(ctx context.Context) {
	if b.warned.CompareAndSwap(false, true) && b.logger != nil {
		b.logger.Warn(ctx, "gateway_backpressure_warn", obslog.Fields{
			Stage:  "P08.5",
			Status: 0,
		})
	}
}

// BackpressureStage performs P08.5.
type BackpressureStage struct {
	d *Deps
}

// NewBackpressureStage builds P08.5.
func NewBackpressureStage(d *Deps) *BackpressureStage { return &BackpressureStage{d: d} }

// Name implements Stage.
func (s *BackpressureStage) Name() string { return StageP085 }

// Run implements Stage. The denominator is read from the snapshot on every
// request so a runtime publish takes effect without a restart.
func (s *BackpressureStage) Run(ctx context.Context, c *Ctx) error {
	// GR-03: settlement backlog is checked before the in-flight slot is
	// taken. The gate only sees requests that have not started yet, so a
	// request already executing is never rejected by a backlog that grew
	// after it entered.
	if s.d.SettleBacklog != nil && s.d.SettleBacklog.Exceeded(ctx) {
		apiErr := apierr.New(apierr.CodeBackpressureRejected)
		if s.d.Logger != nil {
			s.d.Logger.Warn(ctx, "settle_backlog_exceeded", obslog.Fields{
				Stage: "P08.5", RequestID: c.RequestID, Status: apiErr.HTTPStatus,
			})
		}
		return StageErr(StageP085, apiErr)
	}

	max := int64(DefaultMaxInflight)
	if s.d.Config != nil {
		if v, ok := s.d.Config.Int(config.KeyGwMaxInflight); ok && v > 0 {
			max = v
		}
	}
	slot, apiErr := s.d.Backpressure.Acquire(ctx, max, c.Stream)
	if apiErr != nil {
		return StageErr(StageP085, apiErr)
	}
	c.Slot = slot
	return nil
}
