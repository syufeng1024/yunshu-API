package dispatch

import (
	"context"
	"time"

	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
)

// settleTimeout bounds the exit-path settlement. It is deliberately short; a
// failed settlement is logged and left to reconciliation rather than blocking
// the response.
const settleTimeout = 5 * time.Second

// settle releases or consumes the hold. It uses a detached context so a client
// disconnect cannot abandon the money write.
func (d *Dispatcher) settle(ctx context.Context, c *gw.Ctx, consumed int64, reason string) (ledger.SettleResult, error) {
	if d == nil || d.opts.Ledger == nil || c == nil || c.Hold == nil {
		return ledger.SettleResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	params := ledger.SettleParams{
		RequestID:     c.RequestID,
		ConsumedMicro: consumed,
		ReleaseReason: reason,
	}
	if c.Auth != nil {
		params.UserID = c.Auth.UserID
	}
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
