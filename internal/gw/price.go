package gw

import (
	"errors"
	"fmt"

	"yunshu.dev/core/internal/jsonx"
)

// PriceRule is the standard price payload stored at K19 price:{model_id}.
// It never contains a group ratio: K19 is multiplied by K13.gp at read time so
// one cache entry serves every group (TDS §2.1, K19 note).
type PriceRule struct {
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

// ParsePrice decodes one K19 payload. Negative unit prices are rejected rather
// than clamped: a negative price would turn a charge into a credit.
func ParsePrice(raw jsonx.RawMessage) (*PriceRule, error) {
	if len(raw) == 0 {
		return nil, errors.New("gw: empty price payload")
	}
	var p PriceRule
	if err := jsonx.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("gw: decode price: %w", err)
	}
	if p.InPriceMicroPerK < 0 || p.OutPriceMicroPerK < 0 || p.PerCallPriceMicro < 0 {
		return nil, errors.New("gw: price rule contains a negative unit price")
	}
	if p.LongCtxThresholdTokens != nil && *p.LongCtxThresholdTokens < 0 {
		return nil, errors.New("gw: negative long-context threshold")
	}
	return &p, nil
}
