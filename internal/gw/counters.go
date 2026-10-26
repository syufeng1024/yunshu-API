package gw

import (
	"context"
	"errors"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/redisx"
)

// RedisCounters reads the two counters that back the 429 header snapshot.
// It is used only after a rejection, so the extra two GETs never touch the
// allowed path.
type RedisCounters struct {
	Client *redisx.Client
}

// UsedRPM returns the current-minute RPM counter.
func (r RedisCounters) UsedRPM(ctx context.Context, keyID, minEpoch int64) int64 {
	return r.get(ctx, redisx.RPMLKey(keyID, minEpoch))
}

// UsedDaily returns the account's daily micro-credit counter.
func (r RedisCounters) UsedDaily(ctx context.Context, userID int64, day string) int64 {
	return r.get(ctx, redisx.DailyQuotaKey(userID, day))
}

func (r RedisCounters) get(ctx context.Context, key string) int64 {
	if r.Client == nil {
		return 0
	}
	cctx, cancel := r.Client.CommandContext(ctx)
	defer cancel()
	value, err := r.Client.Raw().Get(cctx, key).Int64()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return 0
	}
	if value < 0 {
		return 0
	}
	return value
}
