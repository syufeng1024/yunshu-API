package gw

import (
	"context"
	"errors"

	"yunshu.dev/core/internal/apierr"
	obslog "yunshu.dev/core/internal/obs/log"
)

// Stage is one pipeline step. Name returns the P-number used in logs and in
// the fail_stage field; it is not a call-order index.
type Stage interface {
	Name() string
	Run(ctx context.Context, c *Ctx) error
}

// Stage names used by the pipeline and the tests.
const (
	StageP05  = "P05"
	StageP085 = "P08.5"
	StageP12  = "P12"
	StageP09  = "P09"
	StageP11  = "P11"
	StageP10  = "P10"
)

// Pipeline runs the stages in slice order. The order is fixed by
// NewDefaultPipeline and is deliberately not the numeric order.
type Pipeline struct {
	stages []Stage
	logger *obslog.Logger
}

// NewPipeline accepts the stages in execution order. It is exported for the
// order assertion test; production callers use NewDefaultPipeline.
func NewPipeline(logger *obslog.Logger, stages ...Stage) *Pipeline {
	return &Pipeline{stages: stages, logger: logger}
}

// Names returns the stage names in execution order.
func (p *Pipeline) Names() []string {
	out := make([]string, 0, len(p.stages))
	for _, s := range p.stages {
		out = append(out, s.Name())
	}
	return out
}

// Run executes every stage until one fails or a replay stops the pipeline.
//
// Execution order (TDS §3.1, Issue 12):
//
//	P05 → P08.5 → P12 → P09 → P11 → P10
//
// P11 (four-dimension limiting) is deliberately after P12. The daily-quota
// dimension is denominated in micro-credits and needs est_micro, which needs a
// price, which hangs off the route chosen by P12. Moving P11 before P12 makes
// the daily-quota check use a missing/zero estimate, which under-enforces the
// account ceiling. P12 is a pure in-memory read (no upstream call, no ledger
// write), so doing it earlier has no side effect. Limiting still precedes every
// money write: P10 is the first stage that touches the ledger.
//
// P06 (IP), P07 (model) and P08 (idempotency) execute inside the P05 slot, in
// numeric order, before backpressure. They are sub-steps rather than slots so
// the six-slot execution order stays the single invariant this card asserts.
func (p *Pipeline) Run(ctx context.Context, c *Ctx) error {
	for _, stage := range p.stages {
		if err := ctx.Err(); err != nil {
			return StageErr("P24", apierr.New(apierr.CodeClientAborted))
		}
		err := stage.Run(ctx, c)
		if err == nil {
			continue
		}
		if errors.Is(err, ErrPipelineStop) {
			return nil
		}
		var se *StageError
		if errors.As(err, &se) {
			if se.Stage == "" {
				se.Stage = stage.Name()
			}
			c.FailStage = se.Stage
			p.logFailure(ctx, c, se)
			return se
		}
		c.FailStage = stage.Name()
		wrapped := StageWrap(stage.Name(), err)
		p.logFailure(ctx, c, wrapped.(*StageError))
		return wrapped
	}
	return nil
}

func (p *Pipeline) logFailure(ctx context.Context, c *Ctx, se *StageError) {
	if p == nil || p.logger == nil {
		return
	}
	status := 0
	if se.API != nil {
		status = se.API.HTTPStatus
	}
	p.logger.Error(ctx, "gateway_stage_failed", obslog.Fields{
		Stage:     se.Stage,
		RequestID: c.RequestID,
		Status:    status,
	})
}

// AdmissionStage is the composite P05 slot. Its children run in numeric order;
// the slot reports "P05" to the pipeline while each child keeps its own
// fail_stage value.
type AdmissionStage struct {
	stages []Stage
}

// NewAdmissionStage builds the P05 slot from P05, P06, P07 and P08.
func NewAdmissionStage(auth, ip, model, idem Stage) *AdmissionStage {
	return &AdmissionStage{stages: []Stage{auth, ip, model, idem}}
}

// Name implements Stage.
func (a *AdmissionStage) Name() string { return StageP05 }

// Run implements Stage.
func (a *AdmissionStage) Run(ctx context.Context, c *Ctx) error {
	for _, s := range a.stages {
		if err := s.Run(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
