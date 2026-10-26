package config

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// K19 price cache. This card provides only the read/write surface; the
// pricing path is implemented by T07/T09.
const (
	RedisPricePrefix = "price:"
	PriceCacheTTL    = 120 * time.Second
)

// PriceCacheKey returns price:{model_id}.
func PriceCacheKey(modelID int64) string {
	return RedisPricePrefix + strconv.FormatInt(modelID, 10)
}

// PriceCache stores the serialized standard price rule for one model.
//
// The cached value is the standard unit price only. A group ratio (K13.gp)
// must be applied at read time:
//
//	effective = cached_standard_price × gp / 10000
//
// Multiplying the ratio into the cached value would require one cache entry
// per (model, group) pair and a fan-out invalidation on every ratio change.
type PriceCache struct {
	Redis Redis
}

// SetStandard writes the standard price payload. An empty payload is
// rejected: a missing key is a miss, an empty cached value is corruption.
func (c *PriceCache) SetStandard(ctx context.Context, modelID int64, standard jsonx.RawMessage, ttl time.Duration) error {
	if c.Redis == nil {
		return fmt.Errorf("config: price cache requires redis")
	}
	if len(standard) == 0 {
		return fmt.Errorf("config: standard price for model %d is empty", modelID)
	}
	if ttl <= 0 {
		ttl = PriceCacheTTL
	}
	return c.Redis.Set(ctx, PriceCacheKey(modelID), string(standard), ttl)
}

// GetStandard reads the standard price payload unchanged. ErrKeyNotFound
// means the caller must fall back to the authoritative source.
func (c *PriceCache) GetStandard(ctx context.Context, modelID int64) (jsonx.RawMessage, error) {
	if c.Redis == nil {
		return nil, fmt.Errorf("config: price cache requires redis")
	}
	raw, err := c.Redis.Get(ctx, PriceCacheKey(modelID))
	if err != nil {
		return nil, err
	}
	return jsonx.RawMessage(raw), nil
}
