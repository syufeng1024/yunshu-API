// Package lock owns the three process-coordination keys: the daily
// reconciliation lock (K21), the expiry-batch lock (K22) and the jobsd
// heartbeat (K25).
//
// K21/K22 are SET NX because exactly one replica may run the job. K25 is not
// a lock: t_job_lock records only the owner, and liveness is a separate
// heartbeat, so a dead owner can be distinguished from a slow one.
package lock

import (
	"context"
	_ "embed"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/redisx"
)

// TTLs from TDS §2.1.
const (
	ReconLockTTL       = 2 * time.Hour
	ExpireBatchLockTTL = 300 * time.Second
	JobHeartbeatTTL    = 60 * time.Second
	JobHeartbeatEvery  = 20 * time.Second
)

//go:embed scripts/renew.lua
var renewScriptSource string

var renewScript = goredis.NewScript(renewScriptSource)

// Locker provides the coordination primitives.
type Locker struct {
	client *redisx.Client
	clock  *redisx.Clock
}

// New builds a locker.
func New(client *redisx.Client, clock *redisx.Clock) *Locker {
	if clock == nil {
		clock = redisx.NewClock(nil, nil)
	}
	return &Locker{client: client, clock: clock}
}

// AcquireRecon takes K21 for the Shanghai calendar day. The day string is
// supplied by the caller so the job and its lock cannot disagree about which
// day they are reconciling.
func (l *Locker) AcquireRecon(ctx context.Context, day, owner string) (bool, error) {
	cctx, cancel := l.client.CommandContext(ctx)
	defer cancel()
	return l.client.Raw().SetNX(cctx, redisx.ReconLockKey(day), owner, ReconLockTTL).Result()
}

// AcquireExpireBatch takes K22.
func (l *Locker) AcquireExpireBatch(ctx context.Context, owner string) (bool, error) {
	cctx, cancel := l.client.CommandContext(ctx)
	defer cancel()
	return l.client.Raw().SetNX(cctx, redisx.ExpireBatchLockKey(), owner, ExpireBatchLockTTL).Result()
}

// RenewExpireBatch extends K22 only for the current owner.
func (l *Locker) RenewExpireBatch(ctx context.Context, owner string) (bool, error) {
	lctx, cancel := l.client.LuaContext(ctx)
	defer cancel()
	result, err := renewScript.Run(lctx, l.client.Raw(),
		[]string{redisx.ExpireBatchLockKey()}, owner, ExpireBatchLockTTL.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// Heartbeat writes K25 with a millisecond timestamp and a 60s TTL.
func (l *Locker) Heartbeat(ctx context.Context, instance string) error {
	cctx, cancel := l.client.CommandContext(ctx)
	defer cancel()
	return l.client.Raw().Set(cctx, redisx.JobHeartbeatKey(instance),
		strconv.FormatInt(l.clock.NowMS(), 10), JobHeartbeatTTL).Err()
}

// Alive reports whether the instance's heartbeat has not expired.
func (l *Locker) Alive(ctx context.Context, instance string) (bool, error) {
	cctx, cancel := l.client.CommandContext(ctx)
	defer cancel()
	n, err := l.client.Raw().Exists(cctx, redisx.JobHeartbeatKey(instance)).Result()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
