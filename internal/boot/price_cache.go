package boot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"yunshu.dev/core/internal/apikey"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// CacheWriter populates K19 (standard price per model) and K13 (API-key auth
// cache).
//
// K19 stores the standard price only. Multiplying the group ratio into the
// cached value would need one entry per (model, group) pair and a fan-out
// invalidation on every ratio change (internal/config/pricecache.go).
type CacheWriter struct {
	DB     *sql.DB
	Prices *config.PriceCache
	Keys   redisx.KeyAuthWriter
	Now    clock.Clock
	Logger *obslog.Logger
}

// priceEntry mirrors gw.PriceRule's JSON contract. It is duplicated for the
// same reason as the routing payload structs: gw's decoder is the consumer and
// its struct carries no exported constructor.
type priceEntry struct {
	ID         int64  `json:"id"`
	ScopeLevel string `json:"scope_level"`
	ModelType  string `json:"model_type"`

	InPriceMicroPerK  int64 `json:"in_price_micro_per_k"`
	OutPriceMicroPerK int64 `json:"out_price_micro_per_k"`
	PerCallPriceMicro int64 `json:"per_call_price_micro"`

	LongCtxThresholdTokens   *int64 `json:"long_ctx_threshold_tokens"`
	LongCtxInPriceMicroPerK  *int64 `json:"long_ctx_in_price_micro_per_k"`
	LongCtxOutPriceMicroPerK *int64 `json:"long_ctx_out_price_micro_per_k"`

	CacheHitRatioBP   *int64 `json:"cache_hit_ratio_bp"`
	CacheWriteRatioBP *int64 `json:"cache_write_ratio_bp"`
}

// Refresh writes both caches and returns the first error encountered. Each
// cache is written item by item: one malformed key does not block the rest.
func (w *CacheWriter) Refresh(ctx context.Context) error {
	if w.DB == nil || w.Prices == nil || w.Keys == nil {
		return errors.New("boot: cache writer requires database, price cache and key cache")
	}
	_, priceErr := w.RefreshPrices(ctx)
	_, keyErr := w.RefreshKeys(ctx)
	return errors.Join(priceErr, keyErr)
}

// RefreshPrices rebuilds K19 for every model with an active MODEL-scope rule.
func (w *CacheWriter) RefreshPrices(ctx context.Context) (int, error) {
	now := w.now().Now().UTC()
	rows, err := w.DB.QueryContext(ctx, `
		SELECT m.id, p.id, p.scope_level, p.model_type,
		       p.in_price_micro_per_k, p.out_price_micro_per_k, p.per_call_price_micro,
		       p.long_ctx_threshold_tokens, p.long_ctx_in_price_micro_per_k,
		       p.long_ctx_out_price_micro_per_k,
		       p.cache_hit_ratio_bp, p.cache_write_ratio_bp
		  FROM t_model m
		  JOIN t_price_rule p
		    ON p.scope_level = 'MODEL' AND p.scope_id = m.id AND p.enabled = 1
		   AND p.effective_from <= ?
		   AND (p.effective_to IS NULL OR p.effective_to > ?)
		 WHERE m.deleted_at IS NULL
		   AND p.id = (
		       SELECT p2.id FROM t_price_rule p2
		        WHERE p2.scope_level = 'MODEL' AND p2.scope_id = m.id AND p2.enabled = 1
		          AND p2.effective_from <= ?
		          AND (p2.effective_to IS NULL OR p2.effective_to > ?)
		        ORDER BY p2.version DESC, p2.id DESC LIMIT 1)
		 ORDER BY m.id`, now, now, now, now)
	if err != nil {
		return 0, fmt.Errorf("boot: load prices: %w", err)
	}
	defer rows.Close()

	written := 0
	for rows.Next() {
		var modelID int64
		var p priceEntry
		var longThreshold, longIn, longOut, cacheHit, cacheWrite sql.NullInt64
		if err := rows.Scan(&modelID, &p.ID, &p.ScopeLevel, &p.ModelType,
			&p.InPriceMicroPerK, &p.OutPriceMicroPerK, &p.PerCallPriceMicro,
			&longThreshold, &longIn, &longOut, &cacheHit, &cacheWrite); err != nil {
			return written, fmt.Errorf("boot: scan price: %w", err)
		}
		p.LongCtxThresholdTokens = nullInt64(longThreshold)
		p.LongCtxInPriceMicroPerK = nullInt64(longIn)
		p.LongCtxOutPriceMicroPerK = nullInt64(longOut)
		p.CacheHitRatioBP = nullInt64(cacheHit)
		p.CacheWriteRatioBP = nullInt64(cacheWrite)

		payload, err := jsonx.Marshal(p)
		if err != nil {
			return written, fmt.Errorf("boot: encode price for model %d: %w", modelID, err)
		}
		if err := w.Prices.SetStandard(ctx, modelID, payload, config.PriceCacheTTL); err != nil {
			return written, fmt.Errorf("boot: write K19 for model %d: %w", modelID, err)
		}
		written++
	}
	if err := rows.Err(); err != nil {
		return written, fmt.Errorf("boot: iterate prices: %w", err)
	}
	return written, nil
}

// RefreshKeys rebuilds K13 for every non-deleted API key.
func (w *CacheWriter) RefreshKeys(ctx context.Context) (int, error) {
	rows, err := w.DB.QueryContext(ctx, `
		SELECT k.id, k.user_id, k.key_hash, k.status, k.key_name, k.group_code,
		       k.expire_at, g.ratio_bp,
		       COALESCE((SELECT MAX(r.id) FROM t_api_key_ip_rule r WHERE r.api_key_id = k.id), 0)
		  FROM t_api_key k
		  LEFT JOIN t_price_group g ON g.code = k.group_code
		 WHERE k.deleted_at IS NULL
		 ORDER BY k.id`)
	if err != nil {
		return 0, fmt.Errorf("boot: load keys: %w", err)
	}
	defer rows.Close()

	written := 0
	var firstErr error
	for rows.Next() {
		var (
			kid, uid, ipm int64
			hash, status  string
			name, group   string
			expire        sql.NullTime
			ratio         sql.NullInt64
		)
		if err := rows.Scan(&kid, &uid, &hash, &status, &name, &group,
			&expire, &ratio, &ipm); err != nil {
			return written, fmt.Errorf("boot: scan key: %w", err)
		}
		hash16, err := apikey.HashPrefix16(hash)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("boot: key %d has an unusable key_hash: %w", kid, err)
			}
			continue
		}
		if !ratio.Valid {
			if firstErr == nil {
				firstErr = fmt.Errorf("boot: key %d references unknown price group %q", kid, group)
			}
			continue
		}
		var expMS int64
		if expire.Valid {
			expMS = expire.Time.UTC().UnixMilli()
		}
		if err := w.Keys.Upsert(ctx, hash16, redisx.KeyAuthRecord{
			UID: uid,
			KID: kid,
			ST:  status,
			IPM: ipm,
			EXP: expMS,
			NT:  "",
			KN:  name,
			GRP: group,
			GP:  ratio.Int64,
		}); err != nil {
			return written, fmt.Errorf("boot: write K13 for key %d: %w", kid, err)
		}
		written++
	}
	if err := rows.Err(); err != nil {
		return written, fmt.Errorf("boot: iterate keys: %w", err)
	}
	return written, firstErr
}

func (w *CacheWriter) now() clock.Clock {
	if w.Now != nil {
		return w.Now
	}
	return clock.Real{}
}

func nullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}
