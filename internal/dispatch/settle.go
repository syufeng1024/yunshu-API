package dispatch

import (
	"context"
	"time"

	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/settle"
)

// settleTimeout bounds the synchronous fallback. It is deliberately short; a
// failed settlement is logged and left to reconciliation rather than blocking
// the response.
const settleTimeout = 5 * time.Second

// enqueueTimeout is the P26 budget. A slower enqueue is treated as a failure
// and the request settles synchronously instead.
const enqueueTimeout = 20 * time.Millisecond

// settle releases or consumes the hold.
//
// P26 order of attempts:
//  1. enqueue t_settle_task (20ms budget);
//  2. if enqueue fails for any reason, synchronously call ledger.Settle in the
//     same request — a task that was not persisted must not leave money HELD;
//  3. if both fail, return the error so the caller answers 503
//     settlement_unavailable.
//
// It uses a detached context so a client disconnect cannot abandon the money
// write.
func (d *Dispatcher) settle(ctx context.Context, c *gw.Ctx, consumed int64, reason string) (ledger.SettleResult, error) {
	if d == nil || c == nil || c.Hold == nil {
		return ledger.SettleResult{}, nil
	}
	if d.opts.Ledger == nil && d.opts.SettleEnqueuer == nil {
		return ledger.SettleResult{}, nil
	}
	if ctx == nil {
		ctx = context.TODO()
	}
	params := ledger.SettleParams{
		RequestID:     c.RequestID,
		ConsumedMicro: consumed,
		ReleaseReason: reason,
	}
	if c.Auth != nil {
		params.UserID = c.Auth.UserID
	}

	if d.opts.SettleEnqueuer != nil {
		enqueueCtx, cancelEnqueue := context.WithTimeout(context.WithoutCancel(ctx), enqueueTimeout)
		err := d.opts.SettleEnqueuer.Enqueue(enqueueCtx, settle.EnqueueParams{
			RequestID:     params.RequestID,
			UserID:        params.UserID,
			ConsumedMicro: params.ConsumedMicro,
			ReleaseReason: params.ReleaseReason,
		})
		cancelEnqueue()
		if err == nil {
			return ledger.SettleResult{
				RequestID:     params.RequestID,
				State:         settle.TaskStatePending,
				ConsumedMicro: consumed,
				PreholdMicro:  c.HoldMicro,
				ReleasedMicro: c.HoldMicro - consumed,
			}, nil
		}
		if d.opts.Logger != nil {
			d.opts.Logger.Warn(ctx, "settle_enqueue_failed_fallback_sync: "+err.Error(),
				obslog.Fields{Stage: "P26", RequestID: c.RequestID})
		}
		// Fall through: the enqueue did not persist the task.
	}

	if d.opts.Ledger == nil {
		return ledger.SettleResult{}, nil
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	result, err := d.opts.Ledger.Settle(settleCtx, params)
	if err != nil && d.opts.Logger != nil {
		d.opts.Logger.Error(settleCtx, "settle_failed: "+err.Error(), obslog.Fields{
			Stage: "P27", RequestID: c.RequestID,
		})
	}
	return result, err
}

// completeIdem records DONE so a retry with the same Idempotency-Key cannot
// execute the upstream a second time. A body over 64 KiB is stored without the
// body: the replay path then returns idempotent_replay_unsupported instead of
// a truncated body (PRD E.2).
func (d *Dispatcher) completeIdem(ctx context.Context, c *gw.Ctx, body []byte) {
	if c == nil || c.Idem == nil || d.opts.Idem == nil {
		return
	}
	if len(body) > maxReplayBody {
		body = nil
	}
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := d.opts.Idem.Complete(completeCtx, c.Idem.Record, idemstore.StateDone, body); err != nil {
		if d.opts.Logger != nil {
			d.opts.Logger.Error(completeCtx, "idempotency_complete_failed: "+err.Error(), obslog.Fields{
				Stage: "P08", RequestID: c.RequestID,
			})
		}
	}
	// Mark done even when the write failed: releaseRequest must not overwrite a
	// successful response with FAILED, which would allow a second charge.
	c.IdemDone = true
}

func (d *Dispatcher) record(a Attempt) {
	if d.opts.AttemptSink != nil {
		d.opts.AttemptSink(a)
	}
}

// SettleRecord converts the in-memory attempt into the P29 row shape. The
// conversion lives here so the settle package does not import dispatch.
func (a Attempt) SettleRecord(createdAt time.Time) settle.AttemptRecord {
	code := ""
	if a.UpstreamCode != 0 {
		code = itoa(a.UpstreamCode)
	}
	return settle.AttemptRecord{
		RequestID:     a.RequestID,
		AttemptIndex:  a.AttemptIndex,
		UserID:        a.UserID,
		ModelID:       a.ModelID,
		RouteID:       a.RouteID,
		ChannelID:     a.ChannelID,
		ChannelName:   a.ChannelNameSnap,
		ProtocolCode:  a.ProtocolCode,
		UpstreamModel: a.UpstreamModel,
		Phase:         a.Phase,
		Result:        a.Result,
		HTTPStatus:    a.HTTPStatus,
		UpstreamCode:  code,
		UpstreamMsg:   a.UpstreamMsg,
		IsRetryable:   a.IsRetryable,
		RetryDecision: a.RetryDecision,
		ConnectMS:     a.ConnectMS,
		FirstByteMS:   a.FirstByteMS,
		TotalMS:       a.TotalMS,
		TokensIn:      a.TokensIn,
		TokensOut:     a.TokensOut,
		BytesIn:       a.BytesIn,
		BytesOut:      a.BytesOut,
		ErrorClass:    a.ErrorClass,
		CreatedAt:     createdAt,
	}
}

func itoa(v int) string {
	// The attempt code is a small non-negative integer; strconv would be
	// correct too, but this keeps the conversion free of a second import.
	if v == 0 {
		return ""
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
