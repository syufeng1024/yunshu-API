package billing

import (
	"errors"
	"time"
)

// Price scope levels. V1.0 resolves "MODEL > CHANNEL"; PROTOCOL is accepted by
// the loader but must be treated as empty by SelectPrice.
const (
	ScopeLevelModel    = "MODEL"
	ScopeLevelProtocol = "PROTOCOL"
	ScopeLevelChannel  = "CHANNEL"
)

// Price errors.
var (
	ErrNoPrice           = errors.New("billing: no active price rule for the requested scope")
	ErrPriceScope        = errors.New("billing: unsupported price scope level")
	ErrNegativePriceUnit = errors.New("billing: unit price must not be negative")
)

// PriceRule mirrors the subset of t_price_rule the domain needs. Pointer
// fields preserve the database NULL semantics: NULL means "not configured",
// which is different from zero.
type PriceRule struct {
	ID         int64
	ScopeLevel string
	ScopeID    int64
	ModelType  string

	InPriceMicroPerK  int64
	OutPriceMicroPerK int64
	PerCallPriceMicro int64

	CacheHitRatioBP   *int64
	CacheWriteRatioBP *int64

	LongCtxThresholdTokens   *int64
	LongCtxInPriceMicroPerK  *int64
	LongCtxOutPriceMicroPerK *int64

	CostInMicroPerK  *int64
	CostOutMicroPerK *int64
	CostPerCallMicro *int64

	Version       int64
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	Enabled       bool
}

// ActiveAt reports whether the rule may be used at now. The interval is
// [effective_from, effective_to); a NULL effective_to means no end.
func (r *PriceRule) ActiveAt(now time.Time) bool {
	if r == nil || !r.Enabled {
		return false
	}
	if now.Before(r.EffectiveFrom.UTC()) {
		return false
	}
	if r.EffectiveTo != nil && !now.UTC().Before(r.EffectiveTo.UTC()) {
		return false
	}
	return true
}

// SelectPrice implements the V1.0 chain "MODEL > CHANNEL". A model-level rule
// that exists but is disabled or outside its effective window is treated as
// missing, so the channel rule can still serve the request. PROTOCOL-level
// pricing is deliberately not part of V1.0.
func SelectPrice(modelRule, channelRule *PriceRule, now time.Time) (*PriceRule, error) {
	if modelRule.ActiveAt(now) {
		if modelRule.ScopeLevel != ScopeLevelModel {
			return nil, ErrPriceScope
		}
		return modelRule, nil
	}
	if channelRule.ActiveAt(now) {
		if channelRule.ScopeLevel != ScopeLevelChannel {
			return nil, ErrPriceScope
		}
		return channelRule, nil
	}
	return nil, ErrNoPrice
}

// ApplyRatioBp applies an integer basis-point multiplier to a unit price:
//
//	actual_unit_price = standard_unit_price × ratio_bp / 10000
//
// The division truncates. This is a unit-price transformation, not an amount
// rounding point; the request total is still rounded exactly once in CostMicro.
func ApplyRatioBp(unitMicroPerK, ratioBP int64) (int64, error) {
	if ratioBP < 0 || ratioBP > BasisPoints {
		return 0, ErrInvalidGroupRatio
	}
	return ApplyRateBp(unitMicroPerK, ratioBP)
}

// ApplyRateBp applies a non-negative basis-point rate without the group-ratio
// upper bound. Prehold ratios (1.2 = 12000 bp) are deliberately above 10000,
// while group ratios are capped at 10000. Keeping the two helpers separate
// prevents an "estimate is a price" category error.
func ApplyRateBp(amountMicro, rateBP int64) (int64, error) {
	if amountMicro < 0 || rateBP < 0 {
		return 0, ErrNegativePriceUnit
	}
	return amountMicro * rateBP / BasisPoints, nil
}

// EffectivePrices returns the group-adjusted unit prices and the per-call
// price for one rule. Image pricing uses the per-call price times the image
// count; the group ratio applies to that price too.
func EffectivePrices(rule *PriceRule, ratioBP int64) (in, out, perCall int64, err error) {
	if rule == nil {
		return 0, 0, 0, ErrNoPrice
	}
	if in, err = ApplyRatioBp(rule.InPriceMicroPerK, ratioBP); err != nil {
		return 0, 0, 0, err
	}
	if out, err = ApplyRatioBp(rule.OutPriceMicroPerK, ratioBP); err != nil {
		return 0, 0, 0, err
	}
	if perCall, err = ApplyRatioBp(rule.PerCallPriceMicro, ratioBP); err != nil {
		return 0, 0, 0, err
	}
	return in, out, perCall, nil
}

// ImageCostMicro is the image billing formula: per-call price × image count.
// It is kept separate from the token formula so an image request can never
// accidentally use per-token rounding.
func ImageCostMicro(perCallPriceMicro, imageCount, ratioBP int64) (int64, error) {
	if imageCount < 0 {
		return 0, ErrNegativeTokens
	}
	price, err := ApplyRatioBp(perCallPriceMicro, ratioBP)
	if err != nil {
		return 0, err
	}
	return price * imageCount, nil
}
