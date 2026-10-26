package billing

import (
	"errors"
	"testing"
	"time"
)

func trialPlan() PlanSpec {
	return PlanSpec{
		Code:            PlanTrial,
		RPM:             60,
		TPM:             126000,
		Concurrency:     22,
		DailyQuotaMicro: ptr(10000000),
		Permanent:       false,
		BonusValidDays:  90,
	}
}

func standardPlan() PlanSpec {
	return PlanSpec{
		Code:            PlanStandard,
		RPM:             300,
		TPM:             630000,
		Concurrency:     110,
		DailyQuotaMicro: ptr(50000000),
		Permanent:       true,
		BonusValidDays:  90,
	}
}

func proPlan() PlanSpec {
	return PlanSpec{
		Code:            PlanPro,
		RPM:             1000,
		TPM:             2100000,
		Concurrency:     367,
		DailyQuotaMicro: ptr(200000000),
		Permanent:       true,
		BonusValidDays:  90,
	}
}

func freeDefault() PlanSpec {
	return PlanSpec{
		Code:            PlanFreeDefault,
		RPM:             60,
		TPM:             60000,
		Concurrency:     5,
		DailyQuotaMicro: nil, // NULL = no upper bound
		Permanent:       true,
		BonusValidDays:  90,
	}
}

func TestTrialFallsBackToDefaultAfterExpiry(t *testing.T) {
	purchased := mustTime(t, "2026-06-01T00:00:00Z")
	trial := trialPlan()
	expiry := EntitlementExpireAt(trial, purchased, 7)
	grant := PlanGrant{Plan: trial, PurchasedAt: purchased, EntitlementExpireAt: expiry}

	// Inside the 7-day entitlement.
	inside := purchased.Add(6 * 24 * time.Hour)
	got := EffectiveLimits([]PlanGrant{grant}, freeDefault(), inside)
	if got.Source != PlanTrial || got.RPM != 60 || got.TPM != 126000 {
		t.Fatalf("inside trial = %+v, want trial limits", got)
	}

	// After expiry the trial grant is inactive; the default plan applies.
	after := purchased.Add(8 * 24 * time.Hour)
	got = EffectiveLimits([]PlanGrant{grant}, freeDefault(), after)
	if got.Source != PlanFreeDefault {
		t.Fatalf("after trial source = %s, want %s", got.Source, PlanFreeDefault)
	}
	if got.DailyQuotaMicro != nil {
		t.Fatalf("free default daily quota = %v, want nil (unlimited)", got.DailyQuotaMicro)
	}
}

func TestPaidPlanDoesNotFallBack(t *testing.T) {
	purchased := mustTime(t, "2026-06-01T00:00:00Z")
	plan := standardPlan()
	if exp := EntitlementExpireAt(plan, purchased, 7); exp != nil {
		t.Fatalf("permanent plan expiry = %v, want nil", exp)
	}
	grant := PlanGrant{Plan: plan, PurchasedAt: purchased, EntitlementExpireAt: nil}

	after := purchased.Add(365 * 24 * time.Hour)
	got := EffectiveLimits([]PlanGrant{grant}, freeDefault(), after)
	if got.Source != PlanStandard || got.RPM != 300 || got.DailyQuotaMicro == nil || *got.DailyQuotaMicro != 50000000 {
		t.Fatalf("paid plan after a year = %+v, want standard limits", got)
	}
}

// Four dimensions take the maximum; they are never summed.
func TestLimitsTakeMaxNotSum(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	trial := PlanGrant{Plan: trialPlan(), PurchasedAt: now.Add(-24 * time.Hour), EntitlementExpireAt: ptrTime(now.Add(5 * 24 * time.Hour))}
	standard := PlanGrant{Plan: standardPlan(), PurchasedAt: now.Add(-time.Hour)}

	got := EffectiveLimits([]PlanGrant{trial, standard}, freeDefault(), now)
	if got.RPM != 300 || got.TPM != 630000 || got.Concurrency != 110 {
		t.Fatalf("limits = %+v, want standard dimensions (max, not sum)", got)
	}
	if got.Source != PlanStandard {
		t.Fatalf("source = %s, want %s", got.Source, PlanStandard)
	}
}

func TestDailyQuotaNilMeansUnlimited(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	unlimited := PlanSpec{Code: PlanStandard, RPM: 300, TPM: 1, Concurrency: 1, DailyQuotaMicro: nil, Permanent: true}
	limited := PlanSpec{Code: PlanPro, RPM: 1000, TPM: 1, Concurrency: 1, DailyQuotaMicro: ptr(100), Permanent: true}

	got := EffectiveLimits([]PlanGrant{
		{Plan: unlimited, PurchasedAt: now},
		{Plan: limited, PurchasedAt: now},
	}, freeDefault(), now)
	if got.DailyQuotaMicro != nil {
		t.Fatalf("daily quota = %v, want nil (one plan is unlimited)", *got.DailyQuotaMicro)
	}
}

func TestSameTierRepurchaseKeepsLimits(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	first := PlanGrant{Plan: standardPlan(), PurchasedAt: now.Add(-time.Hour)}
	second := PlanGrant{Plan: standardPlan(), PurchasedAt: now}

	grants, err := ApplyPlanPurchase([]PlanGrant{first}, second)
	if err != nil {
		t.Fatalf("ApplyPlanPurchase: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %d, want 2 (credits accumulate, limits unchanged)", len(grants))
	}
	got := EffectiveLimits(grants, freeDefault(), now)
	if got.RPM != 300 || got.TPM != 630000 || got.Concurrency != 110 {
		t.Fatalf("limits after repurchase = %+v, want unchanged standard limits", got)
	}
}

func TestCrossTierUpgradeCoversOldPlan(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	old := PlanGrant{Plan: standardPlan(), PurchasedAt: now.Add(-30 * 24 * time.Hour)}
	purchase := PlanGrant{Plan: proPlan(), PurchasedAt: now}

	grants, err := ApplyPlanPurchase([]PlanGrant{old}, purchase)
	if err != nil {
		t.Fatalf("ApplyPlanPurchase: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %d, want 2", len(grants))
	}
	if grants[0].EntitlementExpireAt == nil || !grants[0].EntitlementExpireAt.Equal(now) {
		t.Fatalf("old grant expiry = %v, want cut at %v", grants[0].EntitlementExpireAt, now)
	}
	got := EffectiveLimits(grants, freeDefault(), now)
	if got.RPM != 1000 || got.TPM != 2100000 || got.Concurrency != 367 {
		t.Fatalf("limits after upgrade = %+v, want pro limits", got)
	}

	// Just before the upgrade the old plan was still effective.
	before := now.Add(-time.Second)
	oldOnly := []PlanGrant{{Plan: standardPlan(), PurchasedAt: old.PurchasedAt}}
	got = EffectiveLimits(oldOnly, freeDefault(), before)
	if got.RPM != 300 {
		t.Fatalf("limits before upgrade = %+v, want standard", got)
	}
}

func TestDowngradeRejected(t *testing.T) {
	now := mustTime(t, "2026-06-01T00:00:00Z")
	pro := PlanGrant{Plan: proPlan(), PurchasedAt: now.Add(-time.Hour)}
	standard := PlanGrant{Plan: standardPlan(), PurchasedAt: now}
	if _, err := ApplyPlanPurchase([]PlanGrant{pro}, standard); !errors.Is(err, ErrNoDowngrade) {
		t.Fatalf("err = %v, want ErrNoDowngrade", err)
	}
}

// The two validity periods are independent: a purchase never rewrites a
// previously granted bonus expiry.
func TestBonusExpiryIsSeparateFromEntitlement(t *testing.T) {
	paid := mustTime(t, "2026-06-01T00:00:00Z")
	bonus := BonusExpireAt(paid, 90)
	if got := bonus.Format("2006-01-02"); got != "2026-08-30" {
		t.Fatalf("bonus expiry = %s, want 2026-08-30", got)
	}
	plan := standardPlan()
	if exp := EntitlementExpireAt(plan, paid, 7); exp != nil {
		t.Fatalf("permanent entitlement expiry = %v, want nil", exp)
	}

	grant := PlanGrant{Plan: plan, PurchasedAt: paid, BonusExpiresAt: &bonus}
	grants, err := ApplyPlanPurchase(nil, grant)
	if err != nil {
		t.Fatalf("ApplyPlanPurchase: %v", err)
	}
	if grants[0].BonusExpiresAt == nil || !grants[0].BonusExpiresAt.Equal(bonus) {
		t.Fatalf("bonus expiry changed to %v, want %v", grants[0].BonusExpiresAt, bonus)
	}
}

func ptrTime(v time.Time) *time.Time { return &v }
