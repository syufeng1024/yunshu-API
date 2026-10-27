package billing

import (
	"errors"
	"testing"
)

// EffectivePrices is the only place a group ratio is applied to a rule's three
// unit prices. Truncation is intentional: this is a unit-price transform, the
// single rounding point remains CostMicro.
func TestEffectivePricesAppliesGroupRatio(t *testing.T) {
	rule := &PriceRule{InPriceMicroPerK: 150000, OutPriceMicroPerK: 300000, PerCallPriceMicro: 5000}

	in, out, perCall, err := EffectivePrices(rule, BasisPoints)
	if err != nil {
		t.Fatalf("full ratio: %v", err)
	}
	if in != 150000 || out != 300000 || perCall != 5000 {
		t.Fatalf("full ratio = (%d,%d,%d), want the rule's own prices", in, out, perCall)
	}

	in, out, perCall, err = EffectivePrices(rule, 8000)
	if err != nil {
		t.Fatalf("0.8 ratio: %v", err)
	}
	if in != 120000 || out != 240000 || perCall != 4000 {
		t.Fatalf("0.8 ratio = (%d,%d,%d), want (120000,240000,4000)", in, out, perCall)
	}

	in, out, perCall, err = EffectivePrices(rule, 0)
	if err != nil {
		t.Fatalf("internal group: %v", err)
	}
	if in != 0 || out != 0 || perCall != 0 {
		t.Fatalf("internal group = (%d,%d,%d), want zeros", in, out, perCall)
	}
}

func TestEffectivePricesRejectsBadInput(t *testing.T) {
	if _, _, _, err := EffectivePrices(nil, BasisPoints); !errors.Is(err, ErrNoPrice) {
		t.Fatalf("nil rule err = %v, want ErrNoPrice", err)
	}

	rule := &PriceRule{InPriceMicroPerK: 1000}
	if _, _, _, err := EffectivePrices(rule, BasisPoints+1); !errors.Is(err, ErrInvalidGroupRatio) {
		t.Fatalf("ratio above 100%% err = %v, want ErrInvalidGroupRatio", err)
	}
	if _, _, _, err := EffectivePrices(&PriceRule{InPriceMicroPerK: -1}, BasisPoints); !errors.Is(err, ErrNegativePriceUnit) {
		t.Fatalf("negative input price err = %v, want ErrNegativePriceUnit", err)
	}
	if _, _, _, err := EffectivePrices(&PriceRule{InPriceMicroPerK: 1000, OutPriceMicroPerK: -1}, BasisPoints); !errors.Is(err, ErrNegativePriceUnit) {
		t.Fatalf("negative output price err = %v, want ErrNegativePriceUnit", err)
	}
}

// ImageCostMicro is per-call price × image count; the guards keep a negative
// count or a negative price from silently producing a credit.
func TestImageCostMicroRejectsNegativeOperands(t *testing.T) {
	if _, err := ImageCostMicro(1000, -1, BasisPoints); !errors.Is(err, ErrNegativeTokens) {
		t.Fatalf("negative image count err = %v, want ErrNegativeTokens", err)
	}
	if _, err := ImageCostMicro(-1, 2, BasisPoints); !errors.Is(err, ErrNegativePriceUnit) {
		t.Fatalf("negative per-call price err = %v, want ErrNegativePriceUnit", err)
	}
	got, err := ImageCostMicro(1000, 3, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1500 {
		t.Fatalf("cost = %d, want 1500", got)
	}
}

// ApplyRateBp has no upper bound by design (prehold ratios exceed 100%); the
// group-ratio bound lives in ApplyRatioBp.
func TestApplyRateBpAllowsAboveHundredPercent(t *testing.T) {
	got, err := ApplyRateBp(1000, 12000)
	if err != nil {
		t.Fatalf("120%% rate: %v", err)
	}
	if got != 1200 {
		t.Fatalf("120%% of 1000 = %d, want 1200", got)
	}
	if _, err := ApplyRateBp(-1, BasisPoints); !errors.Is(err, ErrNegativePriceUnit) {
		t.Fatalf("negative amount err = %v, want ErrNegativePriceUnit", err)
	}
}

// TierRank has no rank for FREE_DEFAULT-style unknown codes: they must be
// rejected rather than sorted to the bottom silently.
func TestTierRankRejectsUnknownCode(t *testing.T) {
	want := map[string]int{
		PlanTrial: 1, PlanStandard: 2, PlanPro: 3, PlanEnterprise: 4, PlanFreeDefault: 0,
	}
	for code, rank := range want {
		got, err := TierRank(code)
		if err != nil {
			t.Fatalf("TierRank(%q): %v", code, err)
		}
		if got != rank {
			t.Fatalf("TierRank(%q) = %d, want %d", code, got, rank)
		}
	}
	if _, err := TierRank("MYSTERY_TIER"); !errors.Is(err, ErrUnknownTier) {
		t.Fatalf("unknown tier err = %v, want ErrUnknownTier", err)
	}
}
