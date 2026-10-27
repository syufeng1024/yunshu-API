package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Credit batch types (t_credit_batch.batch_type).
const (
	BatchTypeRecharge     = "RECHARGE"
	BatchTypeBonus        = "BONUS"
	BatchTypeRedeem       = "REDEEM"
	BatchTypePlanGrant    = "PLAN_GRANT"
	BatchTypeCompensation = "COMPENSATION"
)

// CreditBatchParams describes one credit batch created inside a caller-owned
// transaction.
//
// Refundable is the B-02/B-12 contract: only RECHARGE batches may be refunded,
// so every bonus-family batch is written with refundable = 0 and a non-nil
// ExpireAt.
type CreditBatchParams struct {
	UserID      int64
	BatchType   string
	SourceID    *int64
	AmountMicro int64
	Refundable  bool
	ExpireAt    *time.Time
	BatchNo     string
}

// InsertCreditBatch writes one t_credit_batch row. It never touches the
// account: the caller must pair it with a ledger entry and Account.Apply in
// the same transaction (see GrantTx).
func InsertCreditBatch(ctx context.Context, tx *sql.Tx, p CreditBatchParams, now time.Time) (int64, error) {
	if tx == nil {
		return 0, fmt.Errorf("%w: transaction is nil", ErrInvalidParam)
	}
	if p.UserID <= 0 || p.AmountMicro <= 0 {
		return 0, fmt.Errorf("%w: user_id and amount must be positive", ErrInvalidParam)
	}
	refundable := 0
	if p.Refundable {
		refundable = 1
	}
	const q = `INSERT INTO t_credit_batch (
		user_id, batch_no, batch_type, source_id, face_value_micro,
		remaining_micro, consumed_micro, refundable, effective_at, expire_at,
		created_at, updated_at
	) VALUES (?,?,?,?,?,?,0,?,?,?,?,?)`
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		batchNo := p.BatchNo
		if batchNo == "" {
			batchNo = now.UTC().Format("20060102") + randomDigits(6)
		}
		res, err := tx.ExecContext(ctx, q,
			p.UserID, batchNo, p.BatchType, p.SourceID, p.AmountMicro,
			p.AmountMicro, refundable, now.UTC(), p.ExpireAt,
			now.UTC(), now.UTC(),
		)
		if err == nil {
			return res.LastInsertId()
		}
		lastErr = err
		// A caller-supplied batch_no is a contract: never retry it. Only a
		// generated number may collide, and then a fresh one is drawn.
		if !isDuplicate(err) || p.BatchNo != "" {
			return 0, fmt.Errorf("ledger: insert credit batch: %w", err)
		}
	}
	return 0, fmt.Errorf("ledger: insert credit batch: %w", lastErr)
}

// GrantParams is one split credit: a refundable recharge part and a
// non-refundable bonus part, each with its own batch and its own ledger entry.
// Either amount may be zero, but not both.
//
// IdemKeyRecharge / IdemKeyBonus are the uniqueness guard (uk_le_idem). A
// second attempt with the same keys fails with ErrDuplicateIdem instead of
// crediting twice, which is the second line of defence behind the caller's
// state CAS.
type GrantParams struct {
	UserID        int64
	RechargeMicro int64
	BonusMicro    int64

	// BonusExpireAt is mandatory when BonusMicro > 0: a bonus batch without an
	// expiry would silently become a permanent refundable-looking balance.
	BonusExpireAt *time.Time

	BizType    string
	OrderID    *int64
	OperatorID *int64
	Reason     *string

	IdemKeyRecharge string
	IdemKeyBonus    string

	// BonusBatchType overrides the batch family of the non-refundable part
	// (default BONUS). A redeem code uses REDEEM so an operator report can
	// separate promotions from purchases.
	BonusBatchType string

	RechargeBatchNo string
	BonusBatchNo    string
}

// GrantResult reports the identifiers the caller needs for its own audit row.
type GrantResult struct {
	RechargeBatchID   int64
	BonusBatchID      int64
	RechargeEntryID   int64
	BonusEntryID      int64
	BalanceAfterMicro int64
}

// Grant credits one or two batches with one ledger entry each, inside the
// caller's transaction. The account row is locked first, so the two Apply
// calls cannot interleave with a concurrent debit.
//
// Splitting is mandatory (PRD §3.1 / T12-T13 hard constraint 3): the recharge
// part has expire_at NULL and is refundable, the bonus part expires and is
// not. Merging them into one batch makes the refundable basis unusable.
func (s *Store) GrantTx(ctx context.Context, tx *sql.Tx, p GrantParams, now time.Time) (GrantResult, error) {
	var out GrantResult
	if tx == nil {
		return out, fmt.Errorf("%w: transaction is nil", ErrInvalidParam)
	}
	if p.UserID <= 0 {
		return out, fmt.Errorf("%w: user_id must be positive", ErrInvalidParam)
	}
	if p.RechargeMicro < 0 || p.BonusMicro < 0 {
		return out, fmt.Errorf("%w: grant amounts must not be negative", ErrInvalidParam)
	}
	if p.RechargeMicro == 0 && p.BonusMicro == 0 {
		return out, fmt.Errorf("%w: nothing to grant", ErrInvalidParam)
	}
	if p.BonusMicro > 0 {
		if p.BonusExpireAt == nil {
			return out, fmt.Errorf("%w: a bonus batch requires expire_at", ErrInvalidParam)
		}
		if !p.BonusExpireAt.After(now) {
			return out, fmt.Errorf("%w: bonus expire_at must be in the future", ErrInvalidParam)
		}
		if p.IdemKeyBonus == "" {
			return out, fmt.Errorf("%w: bonus idem key is required", ErrInvalidParam)
		}
	}
	if p.RechargeMicro > 0 && p.IdemKeyRecharge == "" {
		return out, fmt.Errorf("%w: recharge idem key is required", ErrInvalidParam)
	}
	if p.BizType == "" {
		return out, fmt.Errorf("%w: biz_type is required", ErrInvalidParam)
	}

	acct, err := LockAccount(ctx, tx, p.UserID)
	if err != nil {
		return out, err
	}
	running := acct.TotalMicro()

	if p.RechargeMicro > 0 {
		batchID, err := InsertCreditBatch(ctx, tx, CreditBatchParams{
			UserID:      p.UserID,
			BatchType:   BatchTypeRecharge,
			SourceID:    p.OrderID,
			AmountMicro: p.RechargeMicro,
			Refundable:  true,
			ExpireAt:    nil, // recharge credits never expire
			BatchNo:     p.RechargeBatchNo,
		}, now)
		if err != nil {
			return out, err
		}
		entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
			UserID:              p.UserID,
			Direction:           DirectionCredit,
			BizType:             p.BizType,
			AmountRechargeMicro: p.RechargeMicro,
			AmountTotalMicro:    p.RechargeMicro,
			BalanceBeforeMicro:  running,
			BalanceAfterMicro:   running + p.RechargeMicro,
			BatchID:             &batchID,
			RechargeOrderID:     p.OrderID,
			IdemKey:             p.IdemKeyRecharge,
			OperatorID:          p.OperatorID,
			Reason:              p.Reason,
			CreatedAt:           now,
		})
		if err != nil {
			return out, err
		}
		if err := acct.Apply(ctx, tx, BalanceDelta{
			RechargeMicro:      p.RechargeMicro,
			TotalRechargeMicro: p.RechargeMicro,
		}, entryID, nil, now); err != nil {
			return out, err
		}
		if err := setBatchLedger(ctx, tx, batchID, entryID, now); err != nil {
			return out, err
		}
		running += p.RechargeMicro
		out.RechargeBatchID = batchID
		out.RechargeEntryID = entryID
	}

	if p.BonusMicro > 0 {
		bonusType := p.BonusBatchType
		if bonusType == "" {
			bonusType = BatchTypeBonus
		}
		batchID, err := InsertCreditBatch(ctx, tx, CreditBatchParams{
			UserID:      p.UserID,
			BatchType:   bonusType,
			SourceID:    p.OrderID,
			AmountMicro: p.BonusMicro,
			Refundable:  false,
			ExpireAt:    p.BonusExpireAt,
			BatchNo:     p.BonusBatchNo,
		}, now)
		if err != nil {
			return out, err
		}
		entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
			UserID:             p.UserID,
			Direction:          DirectionCredit,
			BizType:            p.BizType,
			AmountBonusMicro:   p.BonusMicro,
			AmountTotalMicro:   p.BonusMicro,
			BalanceBeforeMicro: running,
			BalanceAfterMicro:  running + p.BonusMicro,
			BatchID:            &batchID,
			RechargeOrderID:    p.OrderID,
			IdemKey:            p.IdemKeyBonus,
			OperatorID:         p.OperatorID,
			Reason:             p.Reason,
			CreatedAt:          now,
		})
		if err != nil {
			return out, err
		}
		if err := acct.Apply(ctx, tx, BalanceDelta{
			BonusMicro:      p.BonusMicro,
			TotalBonusMicro: p.BonusMicro,
		}, entryID, nil, now); err != nil {
			return out, err
		}
		if err := setBatchLedger(ctx, tx, batchID, entryID, now); err != nil {
			return out, err
		}
		running += p.BonusMicro
		out.BonusBatchID = batchID
		out.BonusEntryID = entryID
	}

	out.BalanceAfterMicro = running
	return out, nil
}

// Grant is GrantTx wrapped in its own transaction, for callers whose whole
// unit of work is the credit (a redeem code, an operator compensation).
func (s *Store) Grant(ctx context.Context, p GrantParams) (GrantResult, error) {
	var out GrantResult
	err := s.withTx(ctx, s.DefaultTimeout, func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		res, err := s.GrantTx(ctx, tx, p, now)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrDuplicateIdem) {
			return GrantResult{}, err
		}
		return GrantResult{}, err
	}
	return out, nil
}

func setBatchLedger(ctx context.Context, tx *sql.Tx, batchID, entryID int64, now time.Time) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE t_credit_batch SET create_ledger_id = ?, updated_at = ? WHERE id = ?`,
		entryID, now.UTC(), batchID,
	); err != nil {
		return fmt.Errorf("ledger: link batch %d to entry %d: %w", batchID, entryID, err)
	}
	return nil
}
