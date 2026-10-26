package gw

import (
	"context"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/idemstore"
	obslog "yunshu.dev/core/internal/obs/log"
)

// idemKeyMaxLen matches t_idempotency_record.idem_key varchar(128). A longer
// value would be truncated by MySQL strict mode into a confusing insert error,
// so it is rejected at the boundary.
const idemKeyMaxLen = 128

// idemScope is the scope column value for the public data plane.
const idemScope = "gateway"

// IdemStage performs P08. Idempotency is optional: a missing Idempotency-Key
// skips the stage entirely (TDS §3.1 / D-06).
type IdemStage struct{ d *Deps }

// NewIdemStage builds P08.
func NewIdemStage(d *Deps) *IdemStage { return &IdemStage{d: d} }

// Name implements Stage.
func (s *IdemStage) Name() string { return "P08" }

// Run implements Stage.
func (s *IdemStage) Run(ctx context.Context, c *Ctx) error {
	key := strings.TrimSpace(c.Header.Get("Idempotency-Key"))
	if key == "" {
		return nil
	}
	if len(key) > idemKeyMaxLen {
		return StageErr("P08", apierr.Newf(apierr.CodeInvalidRequestBody, "Idempotency-Key 超过 128 字符"))
	}
	if s.d.Idem == nil {
		// The card requires idempotency to be wired; a nil store here means the
		// process was started without it. Failing closed is safer than silently
		// accepting a duplicate.
		return StageErr("P08", apierr.New(apierr.CodeInternalError))
	}
	if c.Auth == nil {
		return StageErr("P08", apierr.New(apierr.CodeInternalError))
	}

	hash := BodyHashOf(c.RawBody)
	result, err := s.d.Idem.Begin(ctx, idemstore.BeginInput{
		Scope:     idemScope,
		APIKeyID:  c.Auth.KeyID,
		UserID:    c.Auth.UserID,
		IdemKey:   key,
		BodyHash:  hash,
		RequestID: c.RequestID,
	})
	if err != nil {
		if s.d.Logger != nil {
			s.d.Logger.Error(ctx, "idempotency_begin_failed: "+err.Error(), obslog.Fields{Stage: "P08"})
		}
		return StageErr("P08", apierr.New(apierr.CodeInternalError).WithCause(err))
	}

	switch result.Action {
	case idemstore.ActionNew, idemstore.ActionRetryPrevFailed, idemstore.ActionStaleLock:
		rec := idemstore.Record{}
		if result.Record != nil {
			rec = *result.Record
		}
		c.Idem = &IdemState{Key: key, Hash: hash, Record: rec}
		return nil
	default:
		decision := idemstore.DecideReplay(result, c.Stream)
		c.Replay = &decision
		return ErrPipelineStop
	}
}
