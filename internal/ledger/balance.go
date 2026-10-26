package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Account is the materialized balance row. balance_micro is always
// balance_bonus_micro + balance_recharge_micro; frozen_refund_micro is money
// that is still on the account but may neither be consumed nor refunded again.
type Account struct {
	UserID               int64
	BalanceRechargeMicro int64
	BalanceBonusMicro    int64
	BalanceMicro         int64
	FrozenRefundMicro    int64
	OverdraftLimitMicro  int64
	Version              int64
	LastLedgerID         *int64
}

// AvailableMicro is the only balance admission may use:
//
//	available = bonus + recharge - frozen_refund
//
// The overdraft limit is deliberately not added here (PRD §3.1 rule 5).
func (a *Account) AvailableMicro() int64 {
	return a.BalanceBonusMicro + a.BalanceRechargeMicro - a.FrozenRefundMicro
}

// TotalMicro is the materialized balance without the frozen-refund deduction.
func (a *Account) TotalMicro() int64 {
	return a.BalanceBonusMicro + a.BalanceRechargeMicro
}

// LockAccount reads the account under SELECT ... FOR UPDATE. Every write path
// locks the row first, so the version CAS below is a second line of defence
// against a caller that bypassed the lock.
func LockAccount(ctx context.Context, tx *sql.Tx, userID int64) (*Account, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("%w: user_id must be positive", ErrInvalidParam)
	}
	const q = `SELECT user_id, balance_recharge_micro, balance_bonus_micro,
	                  balance_micro, frozen_refund_micro, overdraft_limit_micro,
	                  version, last_ledger_id
	             FROM t_credit_account
	            WHERE user_id = ?
	            FOR UPDATE`
	var a Account
	err := tx.QueryRowContext(ctx, q, userID).Scan(
		&a.UserID, &a.BalanceRechargeMicro, &a.BalanceBonusMicro,
		&a.BalanceMicro, &a.FrozenRefundMicro, &a.OverdraftLimitMicro,
		&a.Version, &a.LastLedgerID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: user_id=%d", ErrNoAccount, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: lock account: %w", err)
	}
	// The redundant column must already agree; refusing here prevents a
	// corrupted row from silently poisoning subsequent sums.
	if a.BalanceMicro != a.BalanceBonusMicro+a.BalanceRechargeMicro {
		return nil, fmt.Errorf("ledger: account %d balance_micro=%d != bonus+recharge=%d",
			userID, a.BalanceMicro, a.BalanceBonusMicro+a.BalanceRechargeMicro)
	}
	return &a, nil
}

// BalanceDelta is one atomic change to the account. ConsumedMicro and the two
// total_* fields are non-negative counters; the balance fields are signed.
type BalanceDelta struct {
	BonusMicro         int64
	RechargeMicro      int64
	ConsumedMicro      int64
	TotalBonusMicro    int64
	TotalRechargeMicro int64
}

// Apply writes the new balances, bumps the CAS version and enforces the
// overdraft floor in the same UPDATE. The caller must already hold the row
// lock; version is checked again so an accidental lock bypass fails loudly.
//
// The WHERE clause also re-checks the floor:
//
//	balance_bonus + balance_recharge - frozen_refund + delta >= -overdraft_limit
//
// settledAt is non-nil only for settlement; recharge/refund/prehold leave
// last_settled_at untouched.
func (a *Account) Apply(
	ctx context.Context,
	tx *sql.Tx,
	d BalanceDelta,
	lastLedgerID int64,
	settledAt *time.Time,
	now time.Time,
) error {
	if d.ConsumedMicro < 0 || d.TotalBonusMicro < 0 || d.TotalRechargeMicro < 0 {
		return fmt.Errorf("%w: counter deltas must be non-negative", ErrInvalidParam)
	}
	newBonus := a.BalanceBonusMicro + d.BonusMicro
	newRecharge := a.BalanceRechargeMicro + d.RechargeMicro
	newTotal := newBonus + newRecharge

	if newTotal-a.FrozenRefundMicro < -a.OverdraftLimitMicro {
		return fmt.Errorf("%w: user_id=%d new_available=%d floor=%d",
			ErrBelowOverdraft, a.UserID, newTotal-a.FrozenRefundMicro, -a.OverdraftLimitMicro)
	}

	const q = `UPDATE t_credit_account
	              SET balance_bonus_micro = ?,
	                  balance_recharge_micro = ?,
	                  balance_micro = ?,
	                  total_consumed_micro = total_consumed_micro + ?,
	                  total_bonus_micro = total_bonus_micro + ?,
	                  total_recharge_micro = total_recharge_micro + ?,
	                  version = version + 1,
	                  last_ledger_id = ?,
	                  last_settled_at = COALESCE(?, last_settled_at),
	                  updated_at = ?
	              WHERE user_id = ?
	                AND version = ?
	                AND balance_bonus_micro + balance_recharge_micro
	                    - frozen_refund_micro + ? >= -overdraft_limit_micro`
	res, err := tx.ExecContext(ctx, q,
		newBonus, newRecharge, newTotal,
		d.ConsumedMicro, d.TotalBonusMicro, d.TotalRechargeMicro,
		lastLedgerID, settledAt, now.UTC(),
		a.UserID, a.Version, d.BonusMicro+d.RechargeMicro,
	)
	if err != nil {
		return fmt.Errorf("ledger: update account: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ledger: account rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: user_id=%d version=%d", ErrCASConflict, a.UserID, a.Version)
	}

	a.BalanceBonusMicro = newBonus
	a.BalanceRechargeMicro = newRecharge
	a.BalanceMicro = newTotal
	a.Version++
	a.LastLedgerID = &lastLedgerID
	return nil
}
