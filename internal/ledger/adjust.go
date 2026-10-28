package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Manual balance adjustment directions (t_balance_adjust.direction).
const (
	AdjustDirectionIncrease = "INCREASE"
	AdjustDirectionDecrease = "DECREASE"
)

// adjustBatchExpiry is the bonus-family lifetime for an increase. t_credit_batch
// documents BONUS 系 as effective_at + 90 days; a manual increase is a
// non-refundable COMPENSATION batch, so it follows the same rule instead of
// becoming a permanent balance no expiry job will ever clear.
const adjustBatchExpiry = 90 * 24 * time.Hour

// AdjustParams is one manual adjustment posted inside a caller-owned
// transaction. The caller (the review centre) owns t_balance_adjust's state
// machine; this package owns only the money movement.
type AdjustParams struct {
	UserID      int64
	AdjustID    int64
	Direction   string
	AmountMicro int64
	OperatorID  int64
	Reason      string
	// IdemKey is the adjustment's business key. It defaults to
	// "adjust:{AdjustID}"; the unique index uk_le_idem turns a second posting
	// of the same document into ErrDuplicateIdem instead of a second credit.
	IdemKey string
}

// AdjustResult reports what the caller needs for its audit row.
type AdjustResult struct {
	BalanceAfterMicro int64
	BatchID           int64
	EntryIDs          []int64
}

// AdjustTx posts one manual adjustment through the ledger.
//
// Increase reuses GrantTx: one COMPENSATION batch and one CREDIT entry, with
// the t_balance_adjust id in t_credit_batch.source_id (the column 0003 already
// documents as "调额审计 ID"). Decrease cannot reuse Debit: Debit is a
// consumption primitive with its own transaction, no operator/reason columns
// and an overdraft-admitting story. Decrease therefore consumes batches in the
// same FIFO order as settlement and appends one DEBIT entry per batch.
//
// Neither path writes t_credit_account directly; Account.Apply is the only
// balance writer and it runs on the caller's tx, so the business state change
// and the ledger posting commit or roll back together.
func (s *Store) AdjustTx(ctx context.Context, tx *sql.Tx, p AdjustParams, now time.Time) (AdjustResult, error) {
	var out AdjustResult
	if tx == nil {
		return out, fmt.Errorf("%w: transaction is nil", ErrInvalidParam)
	}
	if p.UserID <= 0 || p.AdjustID <= 0 || p.AmountMicro <= 0 || p.OperatorID <= 0 {
		return out, fmt.Errorf("%w: user_id/adjust_id/amount/operator_id are required", ErrInvalidParam)
	}
	if strings.TrimSpace(p.Reason) == "" {
		return out, fmt.Errorf("%w: adjustment reason is required", ErrInvalidParam)
	}
	if p.IdemKey == "" {
		p.IdemKey = fmt.Sprintf("adjust:%d", p.AdjustID)
	}

	switch p.Direction {
	case AdjustDirectionIncrease:
		return s.adjustIncreaseTx(ctx, tx, p, now)
	case AdjustDirectionDecrease:
		return s.adjustDecreaseTx(ctx, tx, p, now)
	default:
		return out, fmt.Errorf("%w: unknown adjustment direction %q", ErrInvalidParam, p.Direction)
	}
}

func (s *Store) adjustIncreaseTx(ctx context.Context, tx *sql.Tx, p AdjustParams, now time.Time) (AdjustResult, error) {
	expireAt := now.UTC().Add(adjustBatchExpiry)
	reason := p.Reason
	operatorID := p.OperatorID
	sourceID := p.AdjustID

	res, err := s.GrantTx(ctx, tx, GrantParams{
		UserID:         p.UserID,
		BonusMicro:     p.AmountMicro,
		BonusExpireAt:  &expireAt,
		BizType:        BizTypeAdjust,
		OperatorID:     &operatorID,
		Reason:         &reason,
		SourceID:       &sourceID,
		IdemKeyBonus:   p.IdemKey,
		BonusBatchType: BatchTypeCompensation,
	}, now)
	if err != nil {
		return AdjustResult{}, err
	}
	return AdjustResult{
		BalanceAfterMicro: res.BalanceAfterMicro,
		BatchID:           res.BonusBatchID,
		EntryIDs:          []int64{res.BonusEntryID},
	}, nil
}

func (s *Store) adjustDecreaseTx(ctx context.Context, tx *sql.Tx, p AdjustParams, now time.Time) (AdjustResult, error) {
	acct, err := LockAccount(ctx, tx, p.UserID)
	if err != nil {
		return AdjustResult{}, err
	}
	if p.AmountMicro > acct.AvailableMicro() {
		return AdjustResult{}, fmt.Errorf("%w: user_id=%d available=%d amount=%d",
			ErrInsufficientCredits, p.UserID, acct.AvailableMicro(), p.AmountMicro)
	}

	out, err := s.deductBatchesTx(ctx, tx, p.UserID, p.AmountMicro, p.AmountMicro, now, s.DeductPolicy)
	if err != nil {
		return AdjustResult{}, err
	}
	if out.BatchBackedMicro != p.AmountMicro {
		// A manual decrease never creates an overdraft: the operator is taking
		// credits back, not advancing money the batches cannot back.
		return AdjustResult{}, fmt.Errorf("%w: user_id=%d amount=%d backed=%d",
			ErrInsufficientBatches, p.UserID, p.AmountMicro, out.BatchBackedMicro)
	}

	before := acct.TotalMicro()
	bonusDelta, rechargeDelta := int64(0), int64(0)
	entryIDs := make([]int64, 0, len(out.Debits))
	reason := p.Reason
	operatorID := p.OperatorID
	var lastEntryID int64

	for _, d := range out.Debits {
		bonusAmount, rechargeAmount := int64(0), int64(0)
		if d.AccountType == AccountTypeBonus {
			bonusAmount = -d.AmountMicro
			bonusDelta -= d.AmountMicro
		} else {
			rechargeAmount = -d.AmountMicro
			rechargeDelta -= d.AmountMicro
		}
		after := before - d.AmountMicro
		batchID := d.BatchID
		entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
			UserID:              p.UserID,
			Direction:           DirectionDebit,
			BizType:             BizTypeAdjust,
			AmountBonusMicro:    bonusAmount,
			AmountRechargeMicro: rechargeAmount,
			AmountTotalMicro:    -d.AmountMicro,
			BalanceBeforeMicro:  before,
			BalanceAfterMicro:   after,
			BatchID:             &batchID,
			IdemKey:             fmt.Sprintf("%s:%d", p.IdemKey, d.BatchID),
			OperatorID:          &operatorID,
			Reason:              &reason,
			CreatedAt:           now,
		})
		if err != nil {
			return AdjustResult{}, err
		}
		lastEntryID = entryID
		entryIDs = append(entryIDs, entryID)
		before = after
	}

	// ConsumedMicro stays untouched: a manual decrease is not metered usage.
	// The batch counters still move because that is the only representation of
	// "this face value has been taken back" the schema has.
	if err := acct.Apply(ctx, tx, BalanceDelta{
		BonusMicro:    bonusDelta,
		RechargeMicro: rechargeDelta,
	}, lastEntryID, nil, now); err != nil {
		return AdjustResult{}, err
	}

	return AdjustResult{
		BalanceAfterMicro: acct.TotalMicro(),
		EntryIDs:          entryIDs,
	}, nil
}
