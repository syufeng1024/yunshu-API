package billing

import "testing"

// G-01: 1200 input + 800 output at p_in=1500 / p_out=4500 must be 5,400 micro.
func TestG01CostMatchesHandCalculation(t *testing.T) {
	got := CostMicro(1200, 800, 1500, 4500)
	if got != 5400 {
		t.Fatalf("CostMicro(1200,800,1500,4500)=%d, want 5400", got)
	}
}

// TDS §0.1 original case: 0.15 credits per 1k tokens.
func TestPrecisionAtSubCreditUnit(t *testing.T) {
	cases := []struct {
		tokensIn int64
		want     int64
	}{
		{1000, 1500},
		{1001, 1502},
	}
	for _, c := range cases {
		got := CostMicro(c.tokensIn, 0, 1500, 0)
		if got != c.want {
			t.Fatalf("CostMicro(%d,0,1500,0)=%d, want %d", c.tokensIn, got, c.want)
		}
		t.Logf("precision: %d tokens × 1500 micro/1k = %d micro", c.tokensIn, got)
	}
}

func TestZeroUsageCostsZero(t *testing.T) {
	if got := CostMicro(0, 0, 1500, 4500); got != 0 {
		t.Fatalf("zero usage cost = %d, want 0", got)
	}
}

func TestLongContextIsWholeOrderJump(t *testing.T) {
	threshold := int64(128000)
	longIn := int64(2000)
	longOut := int64(4000)

	p := CostParams{
		TokensIn:               130000,
		TokensOut:              2000,
		BaseInPriceMicroPerK:   1000,
		BaseOutPriceMicroPerK:  2000,
		LongCtxThresholdTokens: &threshold,
		LongCtxInPrice:         &longIn,
		LongCtxOutPrice:        &longOut,
		GroupRatioBP:           10000,
	}
	res, err := RequestCost(p)
	if err != nil {
		t.Fatalf("RequestCost: %v", err)
	}
	if res.Tier != TierLongCtx {
		t.Fatalf("tier = %s, want %s", res.Tier, TierLongCtx)
	}

	// Whole order: all 130,000 input tokens use 2,000 micro/1k.
	wantWhole := CostMicro(130000, 2000, 2000, 4000)
	if res.CostMicro != wantWhole {
		t.Fatalf("whole-order cost = %d, want %d", res.CostMicro, wantWhole)
	}

	// The wrong segmented implementation would charge the first 128,000 at
	// the base price and only the remainder at the tier price. Assert that the
	// two results differ, so this test distinguishes the implementations.
	segmentedInput := CostMicro(128000, 0, 1000, 0) + CostMicro(2000, 0, 2000, 0)
	segmentedOutput := CostMicro(0, 2000, 1000, 2000)
	segmented := segmentedInput + segmentedOutput
	if wantWhole == segmented {
		t.Fatalf("test is not discriminating: whole=%d segmented=%d", wantWhole, segmented)
	}
	if res.CostMicro != 268000 {
		t.Fatalf("whole-order cost = %d, want 268000", res.CostMicro)
	}
}

func TestLongContextThresholdIsStrictlyGreater(t *testing.T) {
	threshold := int64(128000)
	longIn := int64(2000)
	p := CostParams{
		TokensIn:               128000,
		BaseInPriceMicroPerK:   1000,
		LongCtxThresholdTokens: &threshold,
		LongCtxInPrice:         &longIn,
		GroupRatioBP:           10000,
	}
	res, err := RequestCost(p)
	if err != nil {
		t.Fatalf("RequestCost: %v", err)
	}
	if res.Tier != TierBase {
		t.Fatalf("at exactly the threshold tier = %s, want %s", res.Tier, TierBase)
	}

	p.TokensIn = 128001
	res, err = RequestCost(p)
	if err != nil {
		t.Fatalf("RequestCost: %v", err)
	}
	if res.Tier != TierLongCtx {
		t.Fatalf("at threshold+1 tier = %s, want %s", res.Tier, TierLongCtx)
	}
}

func TestCacheBillingDisabledIgnoresRatios(t *testing.T) {
	hit := int64(5000)
	write := int64(12500)
	got, err := EffectiveInputTokens(1000, 400, 100, &hit, &write, false)
	if err != nil {
		t.Fatalf("EffectiveInputTokens: %v", err)
	}
	if got != 1000 {
		t.Fatalf("disabled cache tokens = %d, want 1000", got)
	}
}

func TestCacheBillingEnabledWeightsInput(t *testing.T) {
	hit := int64(5000)    // 0.5
	write := int64(12500) // 1.25
	got, err := EffectiveInputTokens(1000, 400, 100, &hit, &write, true)
	if err != nil {
		t.Fatalf("EffectiveInputTokens: %v", err)
	}
	// 500 misses + 400×0.5 + 100×1.25 = 500 + 200 + 125 = 825.
	if got != 825 {
		t.Fatalf("enabled cache tokens = %d, want 825", got)
	}
}

func TestCacheBillingNilRatioMeansAllMisses(t *testing.T) {
	got, err := EffectiveInputTokens(1000, 400, 100, nil, nil, true)
	if err != nil {
		t.Fatalf("EffectiveInputTokens: %v", err)
	}
	if got != 1000 {
		t.Fatalf("nil cache ratios tokens = %d, want 1000", got)
	}
}

func TestRequestCostAppliesGroupRatio(t *testing.T) {
	p := CostParams{
		TokensIn:             1000,
		TokensOut:            0,
		BaseInPriceMicroPerK: 1500,
		GroupRatioBP:         8500,
	}
	res, err := RequestCost(p)
	if err != nil {
		t.Fatalf("RequestCost: %v", err)
	}
	// 1500 × 0.85 = 1275 micro/1k; 1000 tokens => 1275.
	if res.CostMicro != 1275 {
		t.Fatalf("group cost = %d, want 1275", res.CostMicro)
	}
}

func TestRequestCostRejectsBadInput(t *testing.T) {
	if _, err := RequestCost(CostParams{TokensIn: -1, GroupRatioBP: 10000}); err != ErrNegativeTokens {
		t.Fatalf("negative tokens err = %v, want ErrNegativeTokens", err)
	}
	if _, err := RequestCost(CostParams{BaseInPriceMicroPerK: -1, GroupRatioBP: 10000}); err != ErrNegativePrice {
		t.Fatalf("negative price err = %v, want ErrNegativePrice", err)
	}
}
