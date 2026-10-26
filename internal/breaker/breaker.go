// Package breaker implements the per-channel circuit breaker (K16), the
// single-flight half-open probe lease (K17) and the 429 cooldown (K23).
//
// K23 is deliberately a separate key and never feeds fc: a 429 is a quota
// answer, not a channel fault. Counting it would trip the breaker on a
// healthy channel that is merely rate-limiting us.
//
// Redis unavailable is fail-open here, the opposite of the ratelimiter: the
// breaker protects upstream capacity, and treating an unreadable state as
// OPEN would reject every channel during a Redis outage.
package breaker

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	log "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/redisx"
)

// State is a K16 st value.
type State string

// States.
const (
	StateClosed   State = "CLOSED"
	StateOpen     State = "OPEN"
	StateHalfOpen State = "HALF_OPEN"
)

// Defaults from TDS §2.2 L5 / §4.1.
const (
	DefaultFailThreshold = int64(5)
	DefaultBaseOpen      = 30 * time.Second
	DefaultMaxOpen       = 600 * time.Second
	DefaultProbeTTL      = 30 * time.Second
	ProbeSuccessTarget   = int64(3)
	DefaultCooldown      = 5 * time.Second
	MaxCooldown          = 60 * time.Second
)

//go:embed scripts/cb_record.lua
var recordScriptSource string

//go:embed scripts/cb_probe.lua
var probeScriptSource string

var (
	recordScript = goredis.NewScript(recordScriptSource)
	probeScript  = goredis.NewScript(probeScriptSource)
)

// Breaker owns no process state: every replica reads and writes K16, so a
// circuit opened by one replica stops traffic on all of them.
type Breaker struct {
	client   *redisx.Client
	clock    *redisx.Clock
	logger   redisx.Logger
	probeTTL time.Duration
}

// New builds a breaker. clock may be nil in production (real clock).
func New(client *redisx.Client, clock *redisx.Clock, logger redisx.Logger) *Breaker {
	if clock == nil {
		clock = redisx.NewClock(nil, nil)
	}
	return &Breaker{client: client, clock: clock, logger: logger, probeTTL: DefaultProbeTTL}
}

// RecordInput is one upstream attempt outcome.
type RecordInput struct {
	ChannelID     int64
	Success       bool
	IsProbe       bool
	FailThreshold int64
	BaseOpen      time.Duration
	MaxOpen       time.Duration
}

// Result is the post-attempt state.
type Result struct {
	State          State
	OpenUntilMS    int64
	BackoffMS      int64
	ProbeSuccesses int64
	FailScore      int64
	Unavailable    bool
}

// Record applies one attempt. A Redis failure is logged and reported as
// CLOSED + Unavailable so the caller admits the request.
func (b *Breaker) Record(ctx context.Context, in RecordInput) Result {
	threshold := in.FailThreshold
	if threshold <= 0 {
		threshold = DefaultFailThreshold
	}
	base := in.BaseOpen
	if base <= 0 {
		base = DefaultBaseOpen
	}
	maxOpen := in.MaxOpen
	if maxOpen < base {
		maxOpen = DefaultMaxOpen
		if maxOpen < base {
			maxOpen = base
		}
	}
	isSuccess, isProbe := int64(0), int64(0)
	if in.Success {
		isSuccess = 1
	}
	if in.IsProbe {
		isProbe = 1
	}
	args := []interface{}{
		b.clock.NowMS(),
		threshold,
		base.Milliseconds(),
		maxOpen.Milliseconds(),
		isSuccess,
		isProbe,
		ProbeSuccessTarget,
	}
	lctx, cancel := b.client.LuaContext(ctx)
	defer cancel()
	raw, err := recordScript.Run(lctx, b.client.Raw(),
		[]string{redisx.BreakerKey(in.ChannelID)}, args...).Result()
	if err != nil {
		if b.logger != nil {
			b.logger.Error(ctx, "circuit_breaker_unavailable: "+err.Error(), log.Fields{Stage: "upstream"})
		}
		return Result{State: StateClosed, Unavailable: true}
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) < 5 {
		if b.logger != nil {
			b.logger.Error(ctx, "circuit_breaker_bad_reply", log.Fields{Stage: "upstream"})
		}
		return Result{State: StateClosed, Unavailable: true}
	}
	return Result{
		State:          State(asString(values[0])),
		OpenUntilMS:    asInt64(values[1]),
		BackoffMS:      asInt64(values[2]),
		ProbeSuccesses: asInt64(values[3]),
		FailScore:      asInt64(values[4]),
	}
}

// TryProbe promotes OPEN to HALF_OPEN when the backoff has elapsed and takes
// the K17 lease. The caller must call ReleaseProbe after the attempt even if
// it panics.
func (b *Breaker) TryProbe(ctx context.Context, channelID int64, requestID string) (bool, State, error) {
	keys := []string{redisx.BreakerKey(channelID), redisx.ProbeKey(channelID)}
	args := []interface{}{b.clock.NowMS(), b.probeTTL.Milliseconds(), requestID}
	lctx, cancel := b.client.LuaContext(ctx)
	defer cancel()
	raw, err := probeScript.Run(lctx, b.client.Raw(), keys, args...).Result()
	if err != nil {
		return false, StateClosed, fmt.Errorf("breaker: probe: %w", err)
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) < 3 {
		return false, StateClosed, fmt.Errorf("breaker: probe returned %T", raw)
	}
	return asInt64(values[0]) == 1, State(asString(values[1])), nil
}

// ReleaseProbe returns the probe lease explicitly. The 30s TTL remains only
// as a crash guard.
func (b *Breaker) ReleaseProbe(ctx context.Context, channelID int64) error {
	cctx, cancel := b.client.CommandContext(ctx)
	defer cancel()
	return b.client.Raw().Del(cctx, redisx.ProbeKey(channelID)).Err()
}

// State returns the stored state and open_until without mutating anything.
func (b *Breaker) State(ctx context.Context, channelID int64) (State, int64, error) {
	cctx, cancel := b.client.CommandContext(ctx)
	defer cancel()
	values, err := b.client.Raw().HMGet(cctx, redisx.BreakerKey(channelID), "st", "open_until").Result()
	if err != nil {
		return StateClosed, 0, err
	}
	state := StateClosed
	if len(values) > 0 {
		if s, ok := values[0].(string); ok && s != "" {
			state = State(s)
		}
	}
	var openUntil int64
	if len(values) > 1 {
		openUntil = asInt64(values[1])
	}
	return state, openUntil, nil
}

// SetCooldown applies K23: Retry-After seconds, default 5s, capped at 60s.
// It never touches K16.
func (b *Breaker) SetCooldown(ctx context.Context, channelID int64, retryAfter time.Duration) (time.Duration, error) {
	if retryAfter <= 0 {
		retryAfter = DefaultCooldown
	}
	if retryAfter > MaxCooldown {
		retryAfter = MaxCooldown
	}
	cctx, cancel := b.client.CommandContext(ctx)
	defer cancel()
	if err := b.client.Raw().Set(cctx, redisx.CooldownKey(channelID), "1", retryAfter).Err(); err != nil {
		return 0, err
	}
	return retryAfter, nil
}

// CooldownRemaining returns 0 when the key is absent or has no expiry.
func (b *Breaker) CooldownRemaining(ctx context.Context, channelID int64) (time.Duration, error) {
	cctx, cancel := b.client.CommandContext(ctx)
	defer cancel()
	ttl, err := b.client.Raw().PTTL(cctx, redisx.CooldownKey(channelID)).Result()
	if err != nil {
		return 0, err
	}
	if ttl < 0 {
		return 0, nil
	}
	return ttl, nil
}

func asInt64(v interface{}) int64 {
	switch value := v.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case string:
		var parsed int64
		_, _ = fmt.Sscan(value, &parsed)
		return parsed
	default:
		return 0
	}
}

func asString(v interface{}) string {
	switch value := v.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}
