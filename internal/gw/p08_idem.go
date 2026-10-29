package gw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/idemstore"
	obslog "yunshu.dev/core/internal/obs/log"
)

// idemKeyMaxLen matches t_idempotency_record.idem_key varchar(128). A longer
// value would be truncated by MySQL strict mode into a confusing insert error,
// so it is rejected at the boundary.
const idemKeyMaxLen = 128

// idemScope is the scope column value for client-supplied Idempotency-Key
// values on the public data plane.
const idemScope = "gateway"

// idemAutoScope is the scope column value for the synthetic T41 key. It must
// not be idemScope: (scope, api_key_id, idem_key) is the unique key of
// t_idempotency_record, so sharing "gateway" would put client keys and
// synthetic keys in one namespace where either could shadow the other.
const idemAutoScope = "gateway-auto"

// idemAutoPrefix marks a synthetic key in the row for operators. The full
// sha256 digest is 64 hex characters, so "auto:" + digest is 69 characters —
// well under idemKeyMaxLen. The digest is therefore never truncated and needs
// no shorter encoding or collision caveat.
const idemAutoPrefix = "auto:"

// IdemStage performs P08. A client-supplied Idempotency-Key is a guarantee and
// is optional (TDS §3.1 / D-06); without one the stage applies the PRD's
// automatic de-duplication over a synthetic key, which can be disabled with
// idem.duplicate_window_seconds=0.
type IdemStage struct{ d *Deps }

// NewIdemStage builds P08.
func NewIdemStage(d *Deps) *IdemStage { return &IdemStage{d: d} }

// Name implements Stage.
func (s *IdemStage) Name() string { return "P08" }

// Run implements Stage.
func (s *IdemStage) Run(ctx context.Context, c *Ctx) error {
	rawKey := strings.TrimSpace(c.Header.Get("Idempotency-Key"))
	auto := rawKey == ""
	if !auto && len(rawKey) > idemKeyMaxLen {
		return StageErr("P08", apierr.Newf(apierr.CodeInvalidRequestBody, "Idempotency-Key 超过 128 字符"))
	}
	if s.d.Idem == nil {
		if auto {
			// Automatic de-duplication is an optimisation, not a promise: with
			// no store wired the feature is simply absent, exactly as before
			// T41. An explicit key still fails closed below.
			return nil
		}
		// The card requires idempotency to be wired; a nil store here means the
		// process was started without it. Failing closed is safer than silently
		// accepting a duplicate.
		return StageErr("P08", apierr.New(apierr.CodeInternalError))
	}
	var window time.Duration
	if auto {
		window = s.d.Idem.DuplicateWindow()
		if window <= 0 {
			// Configuration switch: 0 (or a negative value from a mis-seeded
			// row) turns the whole stage off for key-less requests.
			return nil
		}
	}
	if c.Auth == nil {
		return StageErr("P08", apierr.New(apierr.CodeInternalError))
	}

	hash := BodyHashOf(c.RawBody)
	scope := idemScope
	key := rawKey
	nowMS := int64(0)
	if auto {
		nowMS = s.d.ClockNowMS()
		key = autoIdemKey(c, hash, nowMS, window)
		scope = idemAutoScope
	}

	result, err := s.d.Idem.Begin(ctx, idemstore.BeginInput{
		Scope:     scope,
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

	if auto {
		return s.applyAuto(ctx, c, result, nowMS, window)
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

// applyAuto maps a Begin outcome for the synthetic key. The contract differs
// from the explicit-key path (idemstore.DecideReplay): a hit is
// duplicate_request_in_flight, not request_in_flight, and a replayable unary
// body is returned with X-Idempotent-Replay.
func (s *IdemStage) applyAuto(ctx context.Context, c *Ctx, result idemstore.BeginResult, nowMS int64, window time.Duration) error {
	switch result.Action {
	case idemstore.ActionNew, idemstore.ActionRetryPrevFailed, idemstore.ActionStaleLock:
		rec := idemstore.Record{}
		if result.Record != nil {
			rec = *result.Record
		}
		c.Idem = &IdemState{Key: rec.IdemKey, Hash: rec.BodyHash, Record: rec}
		return nil
	}

	decision := autoReplayDecision(result, c.Stream, nowMS, window)
	c.Replay = &decision
	if result.Action == idemstore.ActionKeyReused {
		// Unreachable by construction: the body hash is part of the synthetic
		// key, so a different body cannot land on the same row. If it happens
		// anyway the row is corrupt; report internal_error and keep evidence.
		if s.d.Logger != nil {
			s.d.Logger.Warn(ctx, "idem_auto_key_reused", obslog.Fields{
				Stage: "P08", RequestID: c.RequestID, Status: decision.Status,
			})
		}
		return ErrPipelineStop
	}
	if s.d.Logger != nil {
		// The body is deliberately absent: only the decision shape is logged.
		s.d.Logger.Info(ctx, "idem_auto_dedup", obslog.Fields{
			Stage:     "P08",
			RequestID: c.RequestID,
			Status:    decision.Status,
			Reason:    autoDedupReason(result.Action, c.Stream, len(decision.Body) > 0),
		})
	}
	return ErrPipelineStop
}

// autoReplayDecision is the auto-dedup response mapping (T41 §3.4).
func autoReplayDecision(result idemstore.BeginResult, stream bool, nowMS int64, window time.Duration) idemstore.ReplayDecision {
	switch result.Action {
	case idemstore.ActionReplay:
		requestID := ""
		var body []byte
		if result.Record != nil {
			requestID = result.Record.RequestID
			body = result.Record.ResponseBody
		}
		if !stream && len(body) > 0 {
			return idemstore.ReplayDecision{
				Status: 200, Body: body, RequestID: requestID, AutoDedup: true,
			}
		}
		// A stream is never buffered, and the dispatcher drops unary bodies over
		// 64 KiB. Neither can be replayed, so the duplicate is refused instead
		// of executing (and charging) a second time.
		return idemstore.ReplayDecision{
			Status: 409, Code: "duplicate_request_in_flight",
			RequestID: requestID, RetryAfter: remainingWindow(nowMS, window), AutoDedup: true,
		}
	case idemstore.ActionWait:
		retry := result.RetryAfter
		if retry <= 0 {
			retry = time.Second
		}
		return idemstore.ReplayDecision{
			Status: 409, Code: "duplicate_request_in_flight",
			RetryAfter: retry, AutoDedup: true,
		}
	case idemstore.ActionKeyReused:
		return idemstore.ReplayDecision{Status: 500, Code: "idempotency_auto_key_reused"}
	default:
		return idemstore.ReplayDecision{Status: 500, Code: "idempotency_state_unknown"}
	}
}

// autoIdemKey implements the PRD synthetic key:
//
//	sha256(user_id | api_key_id | model | body_hash | floor(now / window))
//
// floor() rounds into fixed buckets, it is not a sliding window: two identical
// requests on opposite sides of a bucket boundary are intentionally not
// de-duplicated.
func autoIdemKey(c *Ctx, bodyHash string, nowMS int64, window time.Duration) string {
	model := ""
	if c.Model != nil {
		model = c.Model.ModelName
	}
	bucket := nowMS * int64(time.Millisecond) / int64(window)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s|%s|%d",
		c.Auth.UserID, c.Auth.KeyID, model, bodyHash, bucket)))
	return idemAutoPrefix + hex.EncodeToString(sum[:])
}

// remainingWindow is the time left in the current floor(now/window) bucket,
// rounded up to at least one second for Retry-After.
func remainingWindow(nowMS int64, window time.Duration) time.Duration {
	windowMS := window.Milliseconds()
	if windowMS <= 0 {
		return time.Second
	}
	remainingMS := windowMS - nowMS%windowMS
	if remainingMS < int64(time.Second/time.Millisecond) {
		remainingMS = int64(time.Second / time.Millisecond)
	}
	return time.Duration(remainingMS) * time.Millisecond
}

// autoDedupReason is a bounded token set for the fixed log schema.
func autoDedupReason(action idemstore.Action, stream, cachedBody bool) string {
	name := "unknown"
	switch action {
	case idemstore.ActionReplay:
		name = "replay"
	case idemstore.ActionWait:
		name = "wait"
	}
	return fmt.Sprintf("action=%s stream=%t cached_body=%t", name, stream, cachedBody)
}
