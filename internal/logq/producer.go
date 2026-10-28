// Package logq produces the K24 request-log stream.
//
// One request is one message: the request row, its attempt rows, optional
// bodies and the trace id travel together so the consumer never observes a
// half-written timeline. Enqueue failure is counted and logged but never
// changes the request result — losing a log line must not lose a request.
package logq

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/redisx"
)

// DefaultMaxLen is the MAXLEN ~ bound from TDS §2.1 K24. Redis still holds
// rate-limit counters and prehold sentinels; an unbounded log stream would
// eventually evict or OOM them.
const DefaultMaxLen = int64(200_000)

// EntryVersion is the current payload schema version written to field `v`.
const EntryVersion = "1"

// Metrics is the optional observability surface. A nil value disables
// recording; a metric failure never changes the producer's result.
type Metrics interface {
	LogWritten()
	LogDropped(reason string)
}

// Producer appends to log:stream.
type Producer struct {
	client  *redisx.Client
	stream  string
	maxLen  int64
	Metrics Metrics
}

// New builds a producer. maxLen <= 0 uses DefaultMaxLen.
func New(client *redisx.Client, maxLen int64) *Producer {
	if maxLen <= 0 {
		maxLen = DefaultMaxLen
	}
	return &Producer{client: client, stream: redisx.LogStreamKey(), maxLen: maxLen}
}

// Add appends one entry. payload is the serialized Entry (jsonx output).
// Enqueue failure is counted and returned; it never changes the request result
// (TDS §5.2: losing a log line must not lose a request).
func (p *Producer) Add(ctx context.Context, payload []byte) error {
	if len(payload) == 0 {
		p.drop("bad_payload")
		return errors.New("logq: empty payload")
	}
	cctx, cancel := p.client.CommandContext(ctx)
	defer cancel()
	err := p.client.Raw().XAdd(cctx, &goredis.XAddArgs{
		Stream: p.stream,
		MaxLen: p.maxLen,
		Approx: true,
		Values: map[string]interface{}{
			"v": EntryVersion,
			"p": string(payload),
		},
	}).Err()
	if err != nil {
		// Every XAdd error here is a Redis-side failure; the consumer-only
		// reasons (max_deliveries, unknown_version) are not produced here.
		p.drop("redis_down")
		return err
	}
	if p.Metrics != nil {
		p.Metrics.LogWritten()
	}
	return nil
}

func (p *Producer) drop(reason string) {
	if p != nil && p.Metrics != nil {
		p.Metrics.LogDropped(reason)
	}
}

// Backlog returns the current stream length. It is a monitoring read, not a
// consumer: XADD failure stays visible through gw_log_dropped_total, while a
// growing backlog is what gw_log_backlog watches.
func (p *Producer) Backlog(ctx context.Context) (int64, error) {
	if p == nil || p.client == nil {
		return 0, nil
	}
	cctx, cancel := p.client.CommandContext(ctx)
	defer cancel()
	n, err := p.client.Raw().XLen(cctx, p.stream).Result()
	if err != nil {
		return 0, fmt.Errorf("logq: XLEN: %w", err)
	}
	return n, nil
}

// ClaimStale reclaims messages idle for at least minIdle. The 6.2
// auto-claim command is forbidden by the 5.0.9 capability boundary; the
// XPENDING extended form plus XCLAIM is the 5.0 equivalent.
func (p *Producer) ClaimStale(ctx context.Context, group, consumer string, minIdle time.Duration, count int64) ([]goredis.XMessage, error) {
	if count <= 0 {
		count = 100
	}
	cctx, cancel := p.client.CommandContext(ctx)
	defer cancel()
	pending, err := p.client.Raw().XPendingExt(cctx, &goredis.XPendingExtArgs{
		Stream: p.stream,
		Group:  group,
		Start:  "-",
		End:    "+",
		Count:  count,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("logq: XPENDING: %w", err)
	}
	ids := make([]string, 0, len(pending))
	for _, item := range pending {
		if item.Idle >= minIdle {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	messages, err := p.client.Raw().XClaim(cctx, &goredis.XClaimArgs{
		Stream:   p.stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Messages: ids,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("logq: XCLAIM: %w", err)
	}
	return messages, nil
}
