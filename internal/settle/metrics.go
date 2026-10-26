package settle

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// P30 metric accumulation. K14/K15 are per-minute counters; the TTL only
// bounds garbage after a traffic stop. 300s is intentionally longer than one
// minute so a late flush still lands in the same bucket.
const (
	MetricTTL         = 300 * time.Second
	DefaultMetricTick = time.Second
	metricQueueSize   = 4096
)

// MetricDelta is one request's contribution. ScopeType is USER or KEY.
type MetricDelta struct {
	ScopeType     string
	ScopeID       int64
	ChannelID     int64
	MinEpoch      int64
	Requests      int64
	Success       int64
	Failed        int64
	TokensIn      int64
	TokensOut     int64
	ConsumedMicro int64
}

type metricKey struct {
	scopeType string
	scopeID   int64
	channelID int64
	minEpoch  int64
}

// Metrics batches K14/K15 HINCRBY writes.
//
// Redis is not a source of money truth (GR-02): every failure here is a WARN,
// never an error returned to the request.
type Metrics struct {
	Redis    *redisx.Client
	Logger   *obslog.Logger
	Clock    clock.Clock
	Interval time.Duration

	ch      chan MetricDelta
	dropped atomic.Int64
}

// NewMetrics builds the accumulator.
func NewMetrics(rdb *redisx.Client, logger *obslog.Logger, clk clock.Clock) *Metrics {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Metrics{
		Redis:    rdb,
		Logger:   logger,
		Clock:    clk,
		Interval: DefaultMetricTick,
		ch:       make(chan MetricDelta, metricQueueSize),
	}
}

// Add enqueues one delta. A full queue drops the sample; metrics are not money.
func (m *Metrics) Add(d MetricDelta) {
	if m == nil {
		return
	}
	select {
	case m.ch <- d:
	default:
		n := m.dropped.Add(1)
		if m.Logger != nil && n%100 == 1 {
			m.Logger.Warn(context.TODO(), fmt.Sprintf("metric_queue_full dropped=%d", n),
				obslog.Fields{Stage: "P30"})
		}
	}
}

// Dropped returns the number of dropped samples.
func (m *Metrics) Dropped() int64 {
	if m == nil {
		return 0
	}
	return m.dropped.Load()
}

// Run flushes once per interval until ctx is cancelled.
func (m *Metrics) Run(ctx context.Context) error {
	if m == nil || m.Redis == nil {
		return nil
	}
	interval := m.Interval
	if interval <= 0 {
		interval = DefaultMetricTick
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			_ = m.Flush(finalCtx)
			cancel()
			return nil
		case <-ticker.C:
			_ = m.Flush(ctx)
		}
	}
}

// Flush drains the queue and applies one pipeline.
func (m *Metrics) Flush(ctx context.Context) error {
	if m == nil || m.Redis == nil {
		return nil
	}
	deltas := m.drain()
	if len(deltas) == 0 {
		return nil
	}
	agg := map[metricKey]*MetricDelta{}
	for _, d := range deltas {
		if d.MinEpoch == 0 {
			d.MinEpoch = redisx.MinEpochFromMS(m.now().UnixMilli())
		}
		if d.ScopeID > 0 && d.ScopeType != "" {
			mergeMetric(agg, metricKey{scopeType: d.ScopeType, scopeID: d.ScopeID, minEpoch: d.MinEpoch}, d)
		}
		if d.ChannelID > 0 {
			mergeMetric(agg, metricKey{channelID: d.ChannelID, minEpoch: d.MinEpoch}, d)
		}
	}

	cmdCtx, cancel := m.Redis.CommandContext(ctx)
	defer cancel()
	pipe := m.Redis.Raw().Pipeline()
	for k, d := range agg {
		key := redisx.MetricKey(k.scopeType, k.scopeID, k.minEpoch)
		if k.channelID > 0 {
			key = redisx.ChannelMetricKey(k.channelID, k.minEpoch)
		}
		hincr(pipe, cmdCtx, key, "req", d.Requests)
		hincr(pipe, cmdCtx, key, "ok", d.Success)
		hincr(pipe, cmdCtx, key, "fail", d.Failed)
		hincr(pipe, cmdCtx, key, "tok_in", d.TokensIn)
		hincr(pipe, cmdCtx, key, "tok_out", d.TokensOut)
		hincr(pipe, cmdCtx, key, "consumed_micro", d.ConsumedMicro)
		pipe.Expire(cmdCtx, key, MetricTTL)
	}
	if _, err := pipe.Exec(cmdCtx); err != nil {
		if m.Logger != nil {
			m.Logger.Warn(ctx, "metric_flush_failed: "+err.Error(), obslog.Fields{Stage: "P30"})
		}
		return err
	}
	return nil
}

func (m *Metrics) drain() []MetricDelta {
	var out []MetricDelta
	for {
		select {
		case d := <-m.ch:
			out = append(out, d)
		default:
			return out
		}
	}
}

func (m *Metrics) now() time.Time {
	if m.Clock == nil {
		return time.Now().UTC()
	}
	return m.Clock.Now().UTC()
}

func hincr(pipe goredis.Pipeliner, ctx context.Context, key, field string, v int64) {
	if v == 0 {
		return
	}
	pipe.HIncrBy(ctx, key, field, v)
}

func mergeMetric(agg map[metricKey]*MetricDelta, k metricKey, d MetricDelta) {
	cur, ok := agg[k]
	if !ok {
		copy := d
		agg[k] = &copy
		return
	}
	cur.Requests += d.Requests
	cur.Success += d.Success
	cur.Failed += d.Failed
	cur.TokensIn += d.TokensIn
	cur.TokensOut += d.TokensOut
	cur.ConsumedMicro += d.ConsumedMicro
}
