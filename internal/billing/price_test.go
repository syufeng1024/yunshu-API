package billing

import (
	"errors"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func modelRule(id, in, out int64, from time.Time) *PriceRule {
	return &PriceRule{
		ID:                id,
		ScopeLevel:        ScopeLevelModel,
		ScopeID:           1,
		ModelType:         "TEXT",
		InPriceMicroPerK:  in,
		OutPriceMicroPerK: out,
		EffectiveFrom:     from,
		Enabled:           true,
	}
}

func channelRule(id, in, out int64, from time.Time) *PriceRule {
	r := modelRule(id, in, out, from)
	r.ScopeLevel = ScopeLevelChannel
	return r
}

// A11: model-level wins; a missing model-level falls back to channel-level.
func TestPriceChainModelWinsOverChannel(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	model := modelRule(10, 1000, 2000, now.Add(-time.Hour))
	channel := channelRule(20, 800, 1600, now.Add(-time.Hour))

	got, err := SelectPrice(model, channel, now)
	if err != nil {
		t.Fatalf("SelectPrice: %v", err)
	}
	if got.ID != 10 {
		t.Fatalf("selected rule = %d, want model rule 10", got.ID)
	}
}

func TestPriceChainFallsBackToChannel(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	channel := channelRule(20, 800, 1600, now.Add(-time.Hour))

	got, err := SelectPrice(nil, channel, now)
	if err != nil {
		t.Fatalf("SelectPrice: %v", err)
	}
	if got.ID != 20 {
		t.Fatalf("selected rule = %d, want channel rule 20", got.ID)
	}
}

func TestExpiredModelRuleFallsBackToChannel(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	expired := modelRule(10, 1000, 2000, now.Add(-2*time.Hour))
	to := now.Add(-time.Hour)
	expired.EffectiveTo = &to
	channel := channelRule(20, 800, 1600, now.Add(-time.Hour))

	got, err := SelectPrice(expired, channel, now)
	if err != nil {
		t.Fatalf("SelectPrice: %v", err)
	}
	if got.ID != 20 {
		t.Fatalf("selected rule = %d, want channel rule 20", got.ID)
	}
}

func TestNoActivePrice(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	if _, err := SelectPrice(nil, nil, now); !errors.Is(err, ErrNoPrice) {
		t.Fatalf("err = %v, want ErrNoPrice", err)
	}
}

func TestApplyRatioBp(t *testing.T) {
	got, err := ApplyRatioBp(1500, 8500)
	if err != nil {
		t.Fatalf("ApplyRatioBp: %v", err)
	}
	if got != 1275 {
		t.Fatalf("ApplyRatioBp(1500,8500)=%d, want 1275", got)
	}
	if _, err := ApplyRatioBp(1500, 10001); !errors.Is(err, ErrInvalidGroupRatio) {
		t.Fatalf("ratio > 10000 err = %v, want ErrInvalidGroupRatio", err)
	}
	if _, err := ApplyRatioBp(1500, -1); !errors.Is(err, ErrInvalidGroupRatio) {
		t.Fatalf("negative ratio err = %v, want ErrInvalidGroupRatio", err)
	}
}

func TestInternalGroupRatioZeroPricesAtZero(t *testing.T) {
	got, err := ApplyRatioBp(1500, 0)
	if err != nil {
		t.Fatalf("ApplyRatioBp: %v", err)
	}
	if got != 0 {
		t.Fatalf("internal group price = %d, want 0", got)
	}
}

func TestImageCost(t *testing.T) {
	got, err := ImageCostMicro(5000, 3, 8500)
	if err != nil {
		t.Fatalf("ImageCostMicro: %v", err)
	}
	// 5,000 × 0.85 = 4,250 per image; × 3 = 12,750.
	if got != 12750 {
		t.Fatalf("image cost = %d, want 12750", got)
	}
}

// G-05: a price change only affects requests after effective_from. Historical
// calculations (and therefore historical ledger amounts) are unchanged.
func TestG05PriceChangeDoesNotRewriteHistory(t *testing.T) {
	p1From := mustTime(t, "2026-01-01T00:00:00Z")
	p2From := mustTime(t, "2026-03-01T00:00:00Z")
	before := mustTime(t, "2026-02-01T00:00:00Z")
	after := mustTime(t, "2026-04-01T00:00:00Z")

	v1 := modelRule(101, 1500, 4500, p1From)
	v2 := modelRule(102, 2000, 6000, p2From)

	oldRule, err := SelectPrice(v1, nil, before)
	if err != nil {
		t.Fatalf("select before: %v", err)
	}
	if oldRule.ID != 101 {
		t.Fatalf("before change selected %d, want 101", oldRule.ID)
	}
	oldCost := CostMicro(1000, 500, oldRule.InPriceMicroPerK, oldRule.OutPriceMicroPerK)
	if oldCost != 3750 {
		t.Fatalf("historical cost = %d, want 3750", oldCost)
	}

	newRule, err := SelectPrice(v2, nil, after)
	if err != nil {
		t.Fatalf("select after: %v", err)
	}
	if newRule.ID != 102 {
		t.Fatalf("after change selected %d, want 102", newRule.ID)
	}
	newCost := CostMicro(1000, 500, newRule.InPriceMicroPerK, newRule.OutPriceMicroPerK)
	if newCost != 5000 {
		t.Fatalf("new cost = %d, want 5000", newCost)
	}

	// The old rule row is still there and still selected for the old instant.
	again, err := SelectPrice(v1, nil, before)
	if err != nil {
		t.Fatalf("re-select before: %v", err)
	}
	if again.ID != 101 || oldCost != 3750 {
		t.Fatalf("history changed: rule=%d cost=%d", again.ID, oldCost)
	}
}
