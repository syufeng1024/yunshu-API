package gw

import (
	"context"
	"errors"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
)

// PreholdStage performs P10. It is the first stage that writes money.
//
// The 50ms figure in the stage table is superseded by TDS §1's 200ms
// single-transaction cap (ledger.TxnTimeout), which is 4× the 50ms budget: a
// 50ms hard cap would reject on one ordinary jitter while a second-scale cap
// would lose the protection entirely.
type PreholdStage struct{ d *Deps }

// NewPreholdStage builds P10.
func NewPreholdStage(d *Deps) *PreholdStage { return &PreholdStage{d: d} }

// Name implements Stage.
func (s *PreholdStage) Name() string { return StageP10 }

// Run implements Stage.
//
// Timeout handling (card §4.10): the stage returns 503 and releases its
// connection/lease, but it NEVER releases the hold. After a timeout it is
// impossible to know whether the transaction committed; releasing a hold that
// did not happen would create credits from nothing (ledger guards this too).
// The dangling hold is recovered by the 60s reconciliation job.
func (s *PreholdStage) Run(ctx context.Context, c *Ctx) error {
	if c.Est == nil || c.Model == nil || c.Auth == nil {
		return StageErr(StageP10, apierr.New(apierr.CodeInternalError))
	}
	requestID := c.RequestID
	if requestID == "" {
		requestID = apierr.NewRequestID()
		c.RequestID = requestID
	}

	now := time.UnixMilli(s.d.ClockNowMS()).UTC()
	budgetMS := s.intKey(config.KeyGwClientTimeoutBudget, 120_000)
	if c.Stream {
		budgetMS = s.intKey(config.KeyGwStreamTotalMs, 600_000)
	}
	reserveMS := s.intKey(config.KeyGwSettleReserveMs, 15_000)
	expiresAt := now.Add(time.Duration(budgetMS+reserveMS) * time.Millisecond)

	var routeID *int64
	if c.Route != nil {
		id := c.Route.RouteID
		routeID = &id
	}
	nodeID := s.d.NodeID
	if nodeID == "" {
		nodeID = "gatewayd"
	}

	result, err := s.d.Ledger.Prehold(ctx, ledger.PreholdParams{
		RequestID:      requestID,
		UserID:         c.Auth.UserID,
		APIKeyID:       c.Auth.KeyID,
		ModelID:        c.Model.ID,
		RouteID:        routeID,
		NodeID:         nodeID,
		RawEstMicro:    c.Est.RawMicro,
		EstMicro:       c.Est.EstMicro,
		MaxHoldRatioBP: s.decimalBP(config.KeyBillingPreholdMaxRatio, 5000),
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientCredits) {
			overdraft := float64(s.intKey(config.KeyBillingOverdraftLimitDefault, 50))
			return StageErr(StageP10,
				apierr.NewInsufficientCredits(c.Est.RawMicro, float64(c.AvailableMicro), overdraft))
		}
		if s.d.Logger != nil {
			s.d.Logger.Error(ctx, "prehold_failed: "+err.Error(), obslog.Fields{Stage: "P10"})
		}
		// Deliberately no Settle/Release call: see the timeout note above.
		return StageErr(StageP10, apierr.New(apierr.CodeSettlementUnavailable).WithCause(err))
	}
	c.Hold = &result
	c.HoldMicro = result.HoldMicro
	return nil
}

func (s *PreholdStage) intKey(key config.Key, def int64) int64 {
	if s.d.Config == nil {
		return def
	}
	if v, ok := s.d.Config.Int(key); ok {
		return v
	}
	return def
}

func (s *PreholdStage) decimalBP(key config.Key, def int64) int64 {
	if s.d.Config == nil {
		return def
	}
	if v, ok := s.d.Config.Decimal(key); ok {
		return v.BasisPoints()
	}
	return def
}
