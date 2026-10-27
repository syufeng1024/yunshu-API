package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// FreezeRefundTx moves amount from available to frozen_refund_micro inside the
// caller's transaction. The money is still on the account, but neither
// consumption nor a second refund may use it (B-43).
//
// Freezing writes no ledger entry: no money moved. The refund handler writes
// the t_refund_order row and its audit entry in the same transaction.
func (s *Store) FreezeRefundTx(ctx context.Context, tx *sql.Tx, userID, amount int64) error {
	if tx == nil {
		return fmt.Errorf("%w: transaction is nil", ErrInvalidParam)
	}
	if userID <= 0 || amount <= 0 {
		return fmt.Errorf("%w: user_id and amount must be positive", ErrInvalidParam)
	}
	acct, err := LockAccount(ctx, tx, userID)
	if err != nil {
		return err
	}
	if acct.AvailableMicro() < amount {
		return fmt.Errorf("%w: user_id=%d available=%d freeze=%d",
			ErrInsufficientCredits, userID, acct.AvailableMicro(), amount)
	}
	return acct.Apply(ctx, tx, BalanceDelta{FrozenRefundMicro: amount}, 0, nil, s.now())
}

// ReleaseRefundTx unfreezes amount without touching the balance. It is the
// REJECTED/FAILED path: the refund never paid out, so the account totals must
// not move.
func (s *Store) ReleaseRefundTx(ctx context.Context, tx *sql.Tx, userID, amount int64) error {
	if tx == nil {
		return fmt.Errorf("%w: transaction is nil", ErrInvalidParam)
	}
	if userID <= 0 || amount <= 0 {
		return fmt.Errorf("%w: user_id and amount must be positive", ErrInvalidParam)
	}
	acct, err := LockAccount(ctx, tx, userID)
	if err != nil {
		return err
	}
	if acct.FrozenRefundMicro < amount {
		return fmt.Errorf("%w: user_id=%d frozen=%d release=%d",
			ErrInvalidParam, userID, acct.FrozenRefundMicro, amount)
	}
	return acct.Apply(ctx, tx, BalanceDelta{FrozenRefundMicro: -amount}, 0, nil, s.now())
}
