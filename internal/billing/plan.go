package billing

import (
	"errors"
	"time"
)

// Plan errors.
var (
	ErrNoDowngrade = errors.New("billing: downgrade is not supported in V1.0")
	ErrUnknownTier = errors.New("billing: unknown plan code")
)

// Plan codes.
const (
	PlanTrial       = "TRIAL"
	PlanStandard    = "STANDARD"
	PlanPro         = "PRO"
	PlanEnterprise  = "ENTERPRISE"
	PlanFreeDefault = "FREE_DEFAULT"
)

// PlanSpec is the limit-bearing part of t_plan. DailyQuotaMicro uses nil for
// "no upper bound" (the database uses NULL; never 0, because 0 means "cannot
// use" in quota semantics).
type PlanSpec struct {
	Code            string
	RPM             int64
	TPM             int64
	Concurrency     int64
	DailyQuotaMicro *int64
	Permanent       bool
	BonusValidDays  int64
}

// TierRank orders the four purchasable tiers. FREE_DEFAULT is the fallback,
// not a purchasable tier, and therefore has no rank in this ordering.
func TierRank(code string) (int, error) {
	switch code {
	case PlanTrial:
		return 1, nil
	case PlanStandard:
		return 2, nil
	case PlanPro:
		return 3, nil
	case PlanEnterprise:
		return 4, nil
	case PlanFreeDefault:
		return 0, nil
	default:
		return 0, ErrUnknownTier
	}
}

// PlanGrant is one purchased (or trial) entitlement. EntitlementExpireAt is
// nil for permanent plans. The bonus expiry is tracked separately, on the
// credit batch, because a later upgrade must neither extend nor clear it.
type PlanGrant struct {
	Plan                PlanSpec
	PurchasedAt         time.Time
	EntitlementExpireAt *time.Time
	BonusExpiresAt      *time.Time
}

// Limits is the effective four-dimensional limit set. DailyQuotaMicro nil
// means no account daily quota.
type Limits struct {
	RPM             int64
	TPM             int64
	Concurrency     int64
	DailyQuotaMicro *int64
	Source          string
}

// EffectiveLimits takes the maximum of each of the four dimensions across all
// active grants. It never sums them. If no grant is active (trial expired, or
// no purchase), the default plan applies.
func EffectiveLimits(grants []PlanGrant, defaultPlan PlanSpec, now time.Time) Limits {
	active := make([]PlanGrant, 0, len(grants))
	for _, g := range grants {
		if g.EntitlementExpireAt != nil && !now.UTC().Before(g.EntitlementExpireAt.UTC()) {
			continue
		}
		active = append(active, g)
	}
	if len(active) == 0 {
		return Limits{
			RPM:             defaultPlan.RPM,
			TPM:             defaultPlan.TPM,
			Concurrency:     defaultPlan.Concurrency,
			DailyQuotaMicro: cloneInt64(defaultPlan.DailyQuotaMicro),
			Source:          defaultPlan.Code,
		}
	}

	limits := Limits{
		RPM:             active[0].Plan.RPM,
		TPM:             active[0].Plan.TPM,
		Concurrency:     active[0].Plan.Concurrency,
		DailyQuotaMicro: cloneInt64(active[0].Plan.DailyQuotaMicro),
		Source:          active[0].Plan.Code,
	}
	bestRank, _ := TierRank(active[0].Plan.Code)
	for _, g := range active[1:] {
		if g.Plan.RPM > limits.RPM {
			limits.RPM = g.Plan.RPM
		}
		if g.Plan.TPM > limits.TPM {
			limits.TPM = g.Plan.TPM
		}
		if g.Plan.Concurrency > limits.Concurrency {
			limits.Concurrency = g.Plan.Concurrency
		}
		limits.DailyQuotaMicro = maxDailyQuota(limits.DailyQuotaMicro, g.Plan.DailyQuotaMicro)
		if rank, err := TierRank(g.Plan.Code); err == nil && rank > bestRank {
			bestRank = rank
			limits.Source = g.Plan.Code
		}
	}
	return limits
}

func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// maxDailyQuota: nil means unlimited, so it wins over any numeric value.
func maxDailyQuota(a, b *int64) *int64 {
	switch {
	case a == nil || b == nil:
		return nil
	case *b > *a:
		v := *b
		return &v
	default:
		return a
	}
}

// BonusExpireAt is the bonus grant expiry: paidAt + bonus_valid_days. It is
// computed once per grant and stored on the credit batch; a later upgrade
// must never recompute it.
func BonusExpireAt(paidAt time.Time, bonusValidDays int64) time.Time {
	return paidAt.UTC().AddDate(0, 0, int(bonusValidDays))
}

// EntitlementExpireAt is the limit-spec validity, which is a different clock
// from the bonus expiry. Permanent plans return nil; non-permanent plans
// (TRIAL) expire after trialDays.
func EntitlementExpireAt(plan PlanSpec, purchasedAt time.Time, trialDays int64) *time.Time {
	if plan.Permanent {
		return nil
	}
	t := purchasedAt.UTC().AddDate(0, 0, int(trialDays))
	return &t
}

// ApplyPlanPurchase applies the purchase rules to the existing grants:
//
//   - same tier: append a grant (credits accumulate in their own batches;
//     the limit spec is unchanged);
//   - higher tier: the old grants lose their entitlement at the new purchase
//     instant (cross-tier upgrade covers the old spec);
//   - lower tier: rejected, because V1.0 has no downgrade.
//
// BonusExpiresAt on the returned grants is left untouched.
func ApplyPlanPurchase(existing []PlanGrant, purchase PlanGrant) ([]PlanGrant, error) {
	newRank, err := TierRank(purchase.Plan.Code)
	if err != nil {
		return nil, err
	}
	for _, g := range existing {
		rank, err := TierRank(g.Plan.Code)
		if err != nil {
			return nil, err
		}
		if rank > newRank {
			return nil, ErrNoDowngrade
		}
	}

	updated := make([]PlanGrant, 0, len(existing)+1)
	if newRank > 0 {
		for _, g := range existing {
			rank, _ := TierRank(g.Plan.Code)
			if rank < newRank && (g.EntitlementExpireAt == nil ||
				g.EntitlementExpireAt.After(purchase.PurchasedAt)) {
				cut := purchase.PurchasedAt.UTC()
				g.EntitlementExpireAt = &cut
			}
			updated = append(updated, g)
		}
	} else {
		updated = append(updated, existing...)
	}
	return append(updated, purchase), nil
}
