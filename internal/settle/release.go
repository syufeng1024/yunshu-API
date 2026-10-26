package settle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
)

// ErrReleaseRejected means a full release was requested for a request that has
// no matching CONSUME_PREHOLD ledger row. Releasing it would create credits
// from nothing (T09 hard constraint 3 / ledger A8 guard).
var ErrReleaseRejected = errors.New("settle: release rejected: prehold ledger entry missing")

// Releaser is the P27 entry point. A non-zero consumed amount settles the
// difference; consumed==0 releases the hold in full.
//
// ledger.Settle already performs the same prehold-row check inside its
// transaction. The duplicate check here exists so the refusal is explicit and
// observable (ERROR log + rejected counter) instead of surfacing as a generic
// settlement failure. The transaction check remains the final authority.
type Releaser struct {
	DB      *sql.DB
	Ledger  Settler
	Logger  *obslog.Logger
	rejects atomic.Int64
}

// NewReleaser builds a P27 releaser.
func NewReleaser(db *sql.DB, led Settler, logger *obslog.Logger) *Releaser {
	return &Releaser{DB: db, Ledger: led, Logger: logger}
}

// Rejected returns how many releases were refused in this process. It is
// exposed for tests and metrics; it never feeds back into money decisions.
func (r *Releaser) Rejected() int64 {
	if r == nil {
		return 0
	}
	return r.rejects.Load()
}

// Release performs the difference refund. consumed==0 means the request failed
// and the whole hold must be returned.
func (r *Releaser) Release(ctx context.Context, p ledger.SettleParams) (ledger.SettleResult, error) {
	if r == nil || r.Ledger == nil {
		return ledger.SettleResult{}, errors.New("settle: releaser requires a ledger")
	}
	if p.RequestID == "" {
		return ledger.SettleResult{}, fmt.Errorf("%w: request_id required", ledger.ErrInvalidParam)
	}
	if p.ConsumedMicro < 0 {
		return ledger.SettleResult{}, fmt.Errorf("%w: consumed_micro must be >= 0", ledger.ErrInvalidParam)
	}
	if p.ConsumedMicro == 0 {
		if err := r.requirePreholdEntry(ctx, p.RequestID); err != nil {
			r.rejects.Add(1)
			if r.Logger != nil {
				r.Logger.Error(ctx, "settle_release_rejected: "+err.Error(), obslog.Fields{
					Stage: "P27", RequestID: p.RequestID, Status: 500,
				})
			}
			return ledger.SettleResult{}, err
		}
	}
	return r.Ledger.Settle(ctx, p)
}

func (r *Releaser) requirePreholdEntry(ctx context.Context, requestID string) error {
	if r.DB == nil {
		// No DB handle means the caller bypassed the normal wiring. Refuse
		// rather than release without the guard.
		return fmt.Errorf("%w: request_id=%s (no database)", ErrReleaseRejected, requestID)
	}
	var total int64
	err := r.DB.QueryRowContext(ctx,
		`SELECT amount_total_micro FROM t_ledger_entry WHERE idem_key = ?`,
		"prehold:"+requestID,
	).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: request_id=%s", ErrReleaseRejected, requestID)
	}
	if err != nil {
		return fmt.Errorf("settle: read prehold entry: %w", err)
	}
	if total >= 0 {
		return fmt.Errorf("%w: request_id=%s prehold_total=%d is not a debit",
			ErrReleaseRejected, requestID, total)
	}
	return nil
}
