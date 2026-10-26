package config

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// This file holds the V1.0 implementations of the eight C2 strategy families.
// They are dependency-free on purpose: config must stay importable by every
// business package (T05 hard constraint 1). A later module can register a
// replacement under a new name and switch the configuration value; the
// default names below are the built-ins.

// ---------------------------------------------------------------------------
// billing.deduct_policy
// ---------------------------------------------------------------------------

// BatchKind distinguishes recharge credits from bonus credits.
type BatchKind string

// Credit batch kinds.
const (
	BatchRecharge BatchKind = "recharge"
	BatchBonus    BatchKind = "bonus"
)

// DeductBatch is one credit batch as seen by a deduct policy.
type DeductBatch struct {
	ID       string
	Kind     BatchKind
	Remain   int64
	ExpireAt time.Time // Zero for recharge batches (no expiry).
}

// DeductEntry is one allocation line.
type DeductEntry struct {
	BatchID string
	Amount  int64
}

// ErrInsufficientCredits means the policy could not allocate the full amount.
var ErrInsufficientCredits = errors.New("config: insufficient credits")

// DeductPolicy decides which credit batches fund one deduction.
type DeductPolicy interface {
	Name() string
	Allocate(batches []DeductBatch, amount int64) ([]DeductEntry, error)
}

type fifoExpiryPolicy struct{}

func (fifoExpiryPolicy) Name() string { return EnumFifoExpiry }

// Allocate drains bonus batches first (expiry ascending), then recharge
// batches. The input order is not trusted; a stable copy is sorted.
func (fifoExpiryPolicy) Allocate(batches []DeductBatch, amount int64) ([]DeductEntry, error) {
	if amount < 0 {
		return nil, fmt.Errorf("config: deduct amount must not be negative")
	}
	if amount == 0 {
		return nil, nil
	}
	ordered := make([]DeductBatch, len(batches))
	copy(ordered, batches)
	for _, b := range ordered {
		if b.Remain < 0 {
			return nil, fmt.Errorf("config: batch %s has negative balance", b.ID)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		bi, bj := ordered[i], ordered[j]
		if (bi.Kind == BatchBonus) != (bj.Kind == BatchBonus) {
			return bi.Kind == BatchBonus
		}
		if bi.Kind == BatchBonus && !bi.ExpireAt.Equal(bj.ExpireAt) {
			return bi.ExpireAt.Before(bj.ExpireAt)
		}
		return false
	})

	remaining := amount
	entries := make([]DeductEntry, 0, len(ordered))
	for _, b := range ordered {
		if remaining == 0 {
			break
		}
		if b.Remain == 0 {
			continue
		}
		take := b.Remain
		if take > remaining {
			take = remaining
		}
		entries = append(entries, DeductEntry{BatchID: b.ID, Amount: take})
		remaining -= take
	}
	if remaining != 0 {
		return nil, ErrInsufficientCredits
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// billing.refund_policy
// ---------------------------------------------------------------------------

// RefundScope is the data a refund policy may inspect. Amounts are
// micro-credits; the policy never converts to currency.
type RefundScope struct {
	OrderUnusedRechargeMicro   int64
	AccountUnusedRechargeMicro int64
	BalanceMicro               int64
	CancelRequested            bool
}

// RefundPolicy computes the refundable upper bound.
type RefundPolicy interface {
	Name() string
	QuotableMicro(scope RefundScope) (int64, error)
}

type byOrderRefundPolicy struct{}

func (byOrderRefundPolicy) Name() string { return EnumByOrder }

// QuotableMicro implements "90% of the order's unused recharge credits"
// (TDS §3.6, PRD G-04). The refund formula is stated once here so the
// 100x error recorded as E-09 cannot reappear as a second implementation.
func (byOrderRefundPolicy) QuotableMicro(scope RefundScope) (int64, error) {
	if scope.OrderUnusedRechargeMicro < 0 || scope.AccountUnusedRechargeMicro < 0 || scope.BalanceMicro < 0 {
		return 0, fmt.Errorf("config: refund scope amounts must not be negative")
	}
	return scope.OrderUnusedRechargeMicro * 9 / 10, nil
}

// ---------------------------------------------------------------------------
// pricing.cost_source
// ---------------------------------------------------------------------------

// CostSource describes where upstream unit costs come from. The actual read
// is executed by the pricing domain; the strategy answers only the "which
// source" question that the configuration key selects.
type CostSource interface {
	Name() string
	FromBillImport() bool
}

type manualCostSource struct{}

func (manualCostSource) Name() string         { return EnumManual }
func (manualCostSource) FromBillImport() bool { return false }

// ---------------------------------------------------------------------------
// quota.daily_meter
// ---------------------------------------------------------------------------

// QuotaPhase is the point in a request at which the meter is called.
type QuotaPhase string

// Quota phases.
const (
	PhasePrehold QuotaPhase = "prehold"
	PhaseSettle  QuotaPhase = "settle"
)

// QuotaEvent is one metering call.
type QuotaEvent struct {
	Phase     QuotaPhase
	RequestID string
	Preheld   int64
	Settled   int64
	BizDate   string
}

// DailyQuotaMeter converts a request event into micro-credits charged to the
// daily quota. A settle-phase result may be negative.
type DailyQuotaMeter interface {
	Name() string
	Measure(ev QuotaEvent) int64
}

type preholdMeter struct{}

func (preholdMeter) Name() string { return EnumPrehold }
func (preholdMeter) Measure(ev QuotaEvent) int64 {
	if ev.Phase == PhasePrehold {
		return ev.Preheld
	}
	return ev.Settled - ev.Preheld
}

type settleMeter struct{}

func (settleMeter) Name() string { return EnumSettle }
func (settleMeter) Measure(ev QuotaEvent) int64 {
	if ev.Phase == PhasePrehold {
		return 0
	}
	return ev.Settled
}

// ---------------------------------------------------------------------------
// quota.key_daily_mode
// ---------------------------------------------------------------------------

// MaxCredits means "this dimension does not apply".
const MaxCredits int64 = math.MaxInt64

// KeyQuotaCfg carries the available daily limits. nil means no limit.
type KeyQuotaCfg struct {
	KeyDailyMicro     *int64
	AccountDailyMicro *int64
}

// KeyDailyQuota decides whether a per-key daily quota exists.
type KeyDailyQuota interface {
	Name() string
	Limit(cfg KeyQuotaCfg) int64
}

type noneKeyQuota struct{}

func (noneKeyQuota) Name() string            { return EnumNone }
func (noneKeyQuota) Limit(KeyQuotaCfg) int64 { return MaxCredits }

// ---------------------------------------------------------------------------
// quota.reset_scope
// ---------------------------------------------------------------------------

// ResetScope decides the daily quota Redis key dimension.
type ResetScope interface {
	Name() string
	DailyKey(accountID, keyID int64, day string) string
}

type accountResetScope struct{}

func (accountResetScope) Name() string { return EnumAccount }
func (accountResetScope) DailyKey(accountID, _ int64, day string) string {
	return fmt.Sprintf("rl:dq:%d:%s", accountID, day)
}

// ---------------------------------------------------------------------------
// pricing.tier_chain
// ---------------------------------------------------------------------------

// TierChain is the price lookup order.
type TierChain interface {
	Name() string
	Levels() []string
}

type modelChannelChain struct{}

func (modelChannelChain) Name() string { return EnumTierModelChannel }
func (modelChannelChain) Levels() []string {
	return []string{"MODEL", "CHANNEL"}
}

// ---------------------------------------------------------------------------
// pricing.group_switch_mode
// ---------------------------------------------------------------------------

// GroupSwitchMode decides whether a user may move a key to another price
// group. V1.0 is ops_only: user self-service is disabled.
type GroupSwitchMode interface {
	Name() string
	UserSelfServiceAllowed() bool
}

type opsOnlyGroupSwitch struct{}

func (opsOnlyGroupSwitch) Name() string                 { return EnumGroupOpsOnly }
func (opsOnlyGroupSwitch) UserSelfServiceAllowed() bool { return false }

// init registers the built-in V1.0 implementations. Business packages add
// their own implementations through Register from their init() as well; the
// final gate is CheckStrategyRefs at process startup.
func init() {
	Register(KindDeductPolicy, EnumFifoExpiry, fifoExpiryPolicy{})
	Register(KindRefundPolicy, EnumByOrder, byOrderRefundPolicy{})
	Register(KindCostSource, EnumManual, manualCostSource{})
	Register(KindDailyMeter, EnumPrehold, preholdMeter{})
	Register(KindDailyMeter, EnumSettle, settleMeter{})
	Register(KindKeyDailyQuota, EnumNone, noneKeyQuota{})
	Register(KindResetScope, EnumAccount, accountResetScope{})
	Register(KindTierChain, EnumTierModelChannel, modelChannelChain{})
	Register(KindGroupSwitchMode, EnumGroupOpsOnly, opsOnlyGroupSwitch{})
}

// TaxRateMultiplier is the only read point for pricing.tax_rate_multiplier.
// Its sole consumer is the cost-side calculation; it must not be applied to
// the customer price.
func TaxRateMultiplier(snap *Snapshot) Decimal {
	// TODO(Finance): 税率口径待财务签署（PRD D.1 第 15 项）
	if snap == nil {
		return Decimal(DecimalScale)
	}
	d, ok := snap.Decimal(KeyPricingTaxRateMultiplier)
	if !ok {
		return Decimal(DecimalScale)
	}
	return d
}
