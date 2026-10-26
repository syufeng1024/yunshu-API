// Package balance implements the K26 balance mirror.
//
// The mirror exists only to save the gateway one primary-database round trip
// (gatewayd has no general DB pool). It is never the truth: invalidation is a
// DEL, so the next read goes back to the source. Writing a guessed new value
// with SET would let a concurrent settlement overwrite a newer one.
//
// A missing mirror is not a zero balance. Returning 0 would reject a funded
// user; returning unlimited would admit an unfunded one. Both are money bugs,
// so a second miss is a 503.
package balance

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	log "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/redisx"
)

// ErrUnavailable means the mirror and the source both failed to produce a
// balance. Callers must answer 503, never 0.
var ErrUnavailable = errors.New("balance_mirror_unavailable")

// Source is the authoritative balance reader (t_account materialized balance).
type Source interface {
	// LoadBalance returns the available balance in micro-credits and whether
	// the account exists. (0, false, nil) means "no such account".
	LoadBalance(ctx context.Context, userID int64) (micro int64, found bool, err error)
}

// Mirror reads K26 and falls back to Source on a miss.
type Mirror struct {
	client *redisx.Client
	source Source
	ttl    time.Duration
	logger redisx.Logger
}

// New builds a mirror with the 60s TTL from TDS §1.
func New(client *redisx.Client, source Source, ttl time.Duration, logger redisx.Logger) *Mirror {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &Mirror{client: client, source: source, ttl: ttl, logger: logger}
}

// Get returns the available balance in micro-credits. A cache miss loads the
// source exactly once and refills; a second miss is ErrUnavailable.
func (m *Mirror) Get(ctx context.Context, userID int64) (int64, error) {
	key := redisx.BalanceKey(userID)
	cctx, cancel := m.client.CommandContext(ctx)
	value, err := m.client.Raw().Get(cctx, key).Int64()
	cancel()
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, goredis.Nil) {
		// Redis is down, not the balance. The source is authoritative, so a
		// direct read is still correct — just slower.
		if m.logger != nil {
			m.logger.Warn(ctx, "balance_mirror_read_failed: "+err.Error(), log.Fields{Stage: "billing"})
		}
	}
	return m.loadAndRefill(ctx, userID, key)
}

func (m *Mirror) loadAndRefill(ctx context.Context, userID int64, key string) (int64, error) {
	if m.source == nil {
		return 0, fmt.Errorf("%w: no source configured", ErrUnavailable)
	}
	micro, found, err := m.source.LoadBalance(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("%w: source: %v", ErrUnavailable, err)
	}
	if !found {
		return 0, fmt.Errorf("%w: user %d not found", ErrUnavailable, userID)
	}
	// A source hit is authoritative even when the refill fails; losing the
	// cache only costs the next request another round trip.
	cctx, cancel := m.client.CommandContext(ctx)
	err = m.client.Raw().Set(cctx, key, micro, m.ttl).Err()
	cancel()
	if err != nil && m.logger != nil {
		m.logger.Warn(ctx, "balance_mirror_fill_failed: "+err.Error(), log.Fields{Stage: "billing"})
	}
	return micro, nil
}

// Invalidate deletes K26. It never writes a value: the next Get re-reads the
// source, and the source is always newer than anything this process knows.
func (m *Mirror) Invalidate(ctx context.Context, userID int64) error {
	cctx, cancel := m.client.CommandContext(ctx)
	defer cancel()
	return m.client.Raw().Del(cctx, redisx.BalanceKey(userID)).Err()
}
