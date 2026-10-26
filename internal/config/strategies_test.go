package config

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"yunshu.dev/core/internal/config/i18n"
)

func TestFifoExpiryAllocate(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	batches := []DeductBatch{
		{ID: "recharge-1", Kind: BatchRecharge, Remain: 100},
		{ID: "bonus-late", Kind: BatchBonus, Remain: 30, ExpireAt: now.Add(48 * time.Hour)},
		{ID: "bonus-early", Kind: BatchBonus, Remain: 20, ExpireAt: now.Add(24 * time.Hour)},
	}
	policy := fifoExpiryPolicy{}

	entries, err := policy.Allocate(batches, 40)
	if err != nil {
		t.Fatalf("Allocate(40): %v", err)
	}
	want := []DeductEntry{{BatchID: "bonus-early", Amount: 20}, {BatchID: "bonus-late", Amount: 20}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
	if batches[0].ID != "recharge-1" {
		t.Fatal("Allocate mutated the caller's slice order")
	}

	entries, err = policy.Allocate(batches, 100)
	if err != nil {
		t.Fatalf("Allocate(100): %v", err)
	}
	want = []DeductEntry{
		{BatchID: "bonus-early", Amount: 20},
		{BatchID: "bonus-late", Amount: 30},
		{BatchID: "recharge-1", Amount: 50},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}

	if _, err := policy.Allocate(batches, 151); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("Allocate(151) error = %v, want ErrInsufficientCredits", err)
	}
	if entries, err := policy.Allocate(batches, 0); err != nil || entries != nil {
		t.Fatalf("Allocate(0) = %v, %v", entries, err)
	}
	if _, err := policy.Allocate(batches, -1); err == nil {
		t.Fatal("negative amount was accepted")
	}
}

func TestDailyMeters(t *testing.T) {
	prehold := preholdMeter{}
	if got := prehold.Measure(QuotaEvent{Phase: PhasePrehold, Preheld: 100}); got != 100 {
		t.Fatalf("prehold/prehold = %d, want 100", got)
	}
	if got := prehold.Measure(QuotaEvent{Phase: PhaseSettle, Preheld: 100, Settled: 80}); got != -20 {
		t.Fatalf("prehold/settle = %d, want -20", got)
	}

	settle := settleMeter{}
	if got := settle.Measure(QuotaEvent{Phase: PhasePrehold, Preheld: 100}); got != 0 {
		t.Fatalf("settle/prehold = %d, want 0", got)
	}
	if got := settle.Measure(QuotaEvent{Phase: PhaseSettle, Preheld: 100, Settled: 80}); got != 80 {
		t.Fatalf("settle/settle = %d, want 80", got)
	}
}

func TestRefundByOrder(t *testing.T) {
	policy := byOrderRefundPolicy{}
	got, err := policy.QuotableMicro(RefundScope{OrderUnusedRechargeMicro: 10_000})
	if err != nil {
		t.Fatalf("QuotableMicro: %v", err)
	}
	if got != 9_000 {
		t.Fatalf("quotable = %d, want 9000 (90%%)", got)
	}
	if _, err := policy.QuotableMicro(RefundScope{OrderUnusedRechargeMicro: -1}); err == nil {
		t.Fatal("negative scope was accepted")
	}
}

func TestResetScopeAccount(t *testing.T) {
	scope := accountResetScope{}
	if got := scope.DailyKey(7, 9, "20261026"); got != "rl:dq:7:20261026" {
		t.Fatalf("DailyKey = %q", got)
	}
}

func TestKeyDailyQuotaNone(t *testing.T) {
	quota := noneKeyQuota{}
	limit := int64(100)
	if got := quota.Limit(KeyQuotaCfg{KeyDailyMicro: &limit, AccountDailyMicro: &limit}); got != MaxCredits {
		t.Fatalf("none limit = %d, want MaxCredits", got)
	}
}

func TestTierChainAndGroupSwitch(t *testing.T) {
	chain := modelChannelChain{}
	if got := chain.Levels(); !reflect.DeepEqual(got, []string{"MODEL", "CHANNEL"}) {
		t.Fatalf("levels = %v", got)
	}
	switchMode := opsOnlyGroupSwitch{}
	if switchMode.UserSelfServiceAllowed() {
		t.Fatal("ops_only allowed user self-service")
	}
}

func TestCostSourceManual(t *testing.T) {
	source := manualCostSource{}
	if source.Name() != EnumManual || source.FromBillImport() {
		t.Fatalf("manual source = %q, bill import=%v", source.Name(), source.FromBillImport())
	}
}

// TestTaxRateMultiplierDefaultLocked is acceptance A10. Changing the default
// to anything but 1.00 (including 1.13) makes this test fail.
func TestTaxRateMultiplierDefaultLocked(t *testing.T) {
	meta := metaByKey[KeyPricingTaxRateMultiplier]
	if meta.Default != "1.00" {
		t.Fatalf("%s default = %s, want 1.00", KeyPricingTaxRateMultiplier, meta.Default)
	}
	store := NewStore()
	if got := TaxRateMultiplier(store.Snapshot(NsPricing)); got != Decimal(DecimalScale) {
		t.Fatalf("TaxRateMultiplier = %s, want 1.00", got)
	}
	if got := TaxRateMultiplier(nil); got != Decimal(DecimalScale) {
		t.Fatalf("TaxRateMultiplier(nil) = %s, want neutral 1.00", got)
	}
}

func TestDedicatedCopyConstantSlot(t *testing.T) {
	// The slot exists and stays unfilled until Legal signs off (T05 item 13).
	if i18n.DedicatedCopyPublic != "" {
		t.Fatalf("i18n.DedicatedCopyPublic must remain empty in T05, got %q", i18n.DedicatedCopyPublic)
	}
}
