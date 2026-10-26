package billing

import "errors"

// Cache and tier errors. Callers must surface these as validation failures,
// never silently clamp a bad token count to zero.
var (
	ErrNegativeTokens      = errors.New("billing: token count must not be negative")
	ErrCacheTokensExceedIn = errors.New("billing: cache hit/write tokens exceed input tokens")
	ErrNegativePrice       = errors.New("billing: unit price must not be negative")
	ErrInvalidGroupRatio   = errors.New("billing: group ratio must be within 0..10000 bp")
)

// CostMicro is the single amount rounding point of the whole system:
//
//	cost_micro = (tokens_in_eff × p_in_micro_per_k
//	            + tokens_out   × p_out_micro_per_k + 999) / 1000
//
// Integer division truncates; adding 999 makes it ceil for non-negative
// numerators. There must be no per-token rounding anywhere else: the unit
// price is per 1,000 tokens, so rounding only happens once on the request
// total.
func CostMicro(tokensInEff, tokensOut, inPriceMicroPerK, outPriceMicroPerK int64) int64 {
	return (tokensInEff*inPriceMicroPerK + tokensOut*outPriceMicroPerK + 999) / 1000
}

// EffectiveInputTokens applies the cache weighting to the input token count.
//
//	pricing.cache_billing_enabled = false => tok_in_eff = tok_in and the cache
//	ratio columns are not read at all.
//
// When enabled, a nil ratio means the upstream does not publish cache pricing
// for this model; the PRD defines that as "everything is a miss", so the
// effective count is the raw input count. The weighted terms use integer
// basis points; the cache feature is structurally ready but disabled in V1.0.
func EffectiveInputTokens(
	tokensIn, cacheHitTokens, cacheWriteTokens int64,
	hitRatioBP, writeRatioBP *int64,
	enabled bool,
) (int64, error) {
	if !enabled {
		return tokensIn, nil
	}
	if tokensIn < 0 || cacheHitTokens < 0 || cacheWriteTokens < 0 {
		return 0, ErrNegativeTokens
	}
	if cacheHitTokens+cacheWriteTokens > tokensIn {
		return 0, ErrCacheTokensExceedIn
	}
	if hitRatioBP == nil || writeRatioBP == nil {
		return tokensIn, nil
	}
	if *hitRatioBP < 0 || *writeRatioBP < 0 {
		return 0, ErrNegativePrice
	}
	miss := tokensIn - cacheHitTokens - cacheWriteTokens
	return miss +
		(cacheHitTokens * *hitRatioBP / BasisPoints) +
		(cacheWriteTokens * *writeRatioBP / BasisPoints), nil
}

// TierPrices are the unit prices that apply to one whole request.
type TierPrices struct {
	InPriceMicroPerK  int64
	OutPriceMicroPerK int64
	Tier              string
}

// Tier names recorded in t_usage_record.billing_tier.
const (
	TierBase    = "BASE"
	TierLongCtx = "LONG_CTX"
)

// SelectTierPrices implements the long-context "whole-order jump" rule:
// when tokensIn is strictly greater than the configured threshold, every
// input token uses the long-context price. It is deliberately not a
// progressive/segmented calculation.
func SelectTierPrices(
	tokensIn, baseIn, baseOut int64,
	threshold, longIn, longOut *int64,
) TierPrices {
	if threshold != nil && tokensIn > *threshold {
		in, out := baseIn, baseOut
		if longIn != nil {
			in = *longIn
		}
		if longOut != nil {
			out = *longOut
		}
		return TierPrices{InPriceMicroPerK: in, OutPriceMicroPerK: out, Tier: TierLongCtx}
	}
	return TierPrices{InPriceMicroPerK: baseIn, OutPriceMicroPerK: baseOut, Tier: TierBase}
}

// CostParams is the full input of one request-level charge calculation.
// Every threshold and ratio is injected; the package never reads t_config_item.
type CostParams struct {
	TokensIn         int64
	TokensOut        int64
	CacheHitTokens   int64
	CacheWriteTokens int64

	BaseInPriceMicroPerK  int64
	BaseOutPriceMicroPerK int64

	CacheBillingEnabled bool
	CacheHitRatioBP     *int64
	CacheWriteRatioBP   *int64

	LongCtxThresholdTokens *int64
	LongCtxInPrice         *int64
	LongCtxOutPrice        *int64

	GroupRatioBP int64
}

// CostResult is the auditable result of one request charge.
type CostResult struct {
	CostMicro         int64
	TokensInEff       int64
	Tier              string
	InPriceMicroPerK  int64
	OutPriceMicroPerK int64
	GroupRatioBP      int64
}

// RequestCost combines cache weighting, the long-context tier and the group
// ratio, then calls CostMicro exactly once. It is the only function the gateway
// needs for pricing a successful request.
func RequestCost(p CostParams) (CostResult, error) {
	if p.TokensIn < 0 || p.TokensOut < 0 ||
		p.CacheHitTokens < 0 || p.CacheWriteTokens < 0 {
		return CostResult{}, ErrNegativeTokens
	}
	if p.BaseInPriceMicroPerK < 0 || p.BaseOutPriceMicroPerK < 0 {
		return CostResult{}, ErrNegativePrice
	}

	// The long-context threshold is defined on the raw input token count, not
	// on the cache-weighted equivalent.
	tier := SelectTierPrices(
		p.TokensIn,
		p.BaseInPriceMicroPerK, p.BaseOutPriceMicroPerK,
		p.LongCtxThresholdTokens, p.LongCtxInPrice, p.LongCtxOutPrice,
	)

	tokensInEff, err := EffectiveInputTokens(
		p.TokensIn, p.CacheHitTokens, p.CacheWriteTokens,
		p.CacheHitRatioBP, p.CacheWriteRatioBP, p.CacheBillingEnabled,
	)
	if err != nil {
		return CostResult{}, err
	}

	inPrice, err := ApplyRatioBp(tier.InPriceMicroPerK, p.GroupRatioBP)
	if err != nil {
		return CostResult{}, err
	}
	outPrice, err := ApplyRatioBp(tier.OutPriceMicroPerK, p.GroupRatioBP)
	if err != nil {
		return CostResult{}, err
	}

	return CostResult{
		CostMicro:         CostMicro(tokensInEff, p.TokensOut, inPrice, outPrice),
		TokensInEff:       tokensInEff,
		Tier:              tier.Tier,
		InPriceMicroPerK:  inPrice,
		OutPriceMicroPerK: outPrice,
		GroupRatioBP:      p.GroupRatioBP,
	}, nil
}
