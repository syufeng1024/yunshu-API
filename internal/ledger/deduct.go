package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DeductPolicy names the batch-ordering strategy. V1.0 ships only
// fifo_expiry; the enum is the registry seam required by the task card.
type DeductPolicy string

// DeductPolicyFIFOExpiry is the default: bonus batches by expiry then
// creation, recharge batches FIFO by creation.
const DeductPolicyFIFOExpiry DeductPolicy = "fifo_expiry"

// BatchDebit is one batch-level deduction. One row becomes one DEBIT ledger
// entry carrying batch_id, so the refundable base can always be recomputed.
type BatchDebit struct {
	BatchID     int64
	BatchType   string
	AmountMicro int64
	AccountType string
}

type deductOutcome struct {
	Debits           []BatchDebit
	BatchBackedMicro int64
	LeftMicro        int64
}

// deductBatchesTx consumes batches in the mandated order:
//
//  1. bonus batches (refundable = 0), ordered by expire_at ASC, created_at ASC,
//     id ASC; NULL expiry sorts last;
//  2. recharge batches (refundable = 1), ordered by created_at ASC, id ASC
//     (FIFO).
//
// maxBatchBacked caps how much may be taken from batches. Settlement passes
// the available balance so frozen refund money is never consumed; the remaining
// part is the overdraft portion and gets its own non-batch ledger entry.
// Every batch update is a CAS on the remaining amount.
func (s *Store) deductBatchesTx(
	ctx context.Context,
	tx *sql.Tx,
	userID, amount, maxBatchBacked int64,
	now time.Time,
	policy DeductPolicy,
) (deductOutcome, error) {
	if amount < 0 {
		return deductOutcome{}, fmt.Errorf("%w: amount must not be negative", ErrInvalidParam)
	}
	if policy == "" {
		policy = DeductPolicyFIFOExpiry
	}
	if policy != DeductPolicyFIFOExpiry {
		return deductOutcome{}, fmt.Errorf("%w: unsupported deduct policy %q", ErrInvalidParam, policy)
	}
	toBack := amount
	if maxBatchBacked >= 0 && toBack > maxBatchBacked {
		toBack = maxBatchBacked
	}
	if toBack < 0 {
		toBack = 0
	}

	out := deductOutcome{LeftMicro: toBack}
	if toBack == 0 {
		return out, nil
	}

	left, err := s.deductBatchGroup(ctx, tx, userID, toBack, now, false, &out)
	if err != nil {
		return out, err
	}
	if left > 0 {
		left, err = s.deductBatchGroup(ctx, tx, userID, left, now, true, &out)
		if err != nil {
			return out, err
		}
	}
	out.LeftMicro = left
	return out, nil
}

// deductBatchGroup consumes one refundable class. It returns the amount that
// could not be covered by this class.
func (s *Store) deductBatchGroup(
	ctx context.Context,
	tx *sql.Tx,
	userID, amount int64,
	now time.Time,
	recharge bool,
	out *deductOutcome,
) (int64, error) {
	const bonusSQL = `SELECT id, batch_type, remaining_micro
	                    FROM t_credit_batch
	                   WHERE user_id = ? AND refundable = 0 AND remaining_micro > 0
	                     AND (expire_at IS NULL OR expire_at > ?)
	                   ORDER BY (expire_at IS NULL) ASC, expire_at ASC, created_at ASC, id ASC
	                   FOR UPDATE`
	const rechargeSQL = `SELECT id, batch_type, remaining_micro
	                       FROM t_credit_batch
	                      WHERE user_id = ? AND refundable = 1 AND remaining_micro > 0
	                        AND (expire_at IS NULL OR expire_at > ?)
	                      ORDER BY created_at ASC, id ASC
	                      FOR UPDATE`

	q := bonusSQL
	accountType := AccountTypeBonus
	if recharge {
		q = rechargeSQL
		accountType = AccountTypeRecharge
	}

	rows, err := tx.QueryContext(ctx, q, userID, now.UTC())
	if err != nil {
		return amount, fmt.Errorf("ledger: select credit batches: %w", err)
	}
	defer rows.Close()

	type batchRow struct {
		id        int64
		batchType string
		remaining int64
	}
	var batches []batchRow
	for rows.Next() {
		var b batchRow
		if err := rows.Scan(&b.id, &b.batchType, &b.remaining); err != nil {
			return amount, fmt.Errorf("ledger: scan credit batch: %w", err)
		}
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return amount, fmt.Errorf("ledger: iterate credit batches: %w", err)
	}
	// Close before issuing UPDATEs: the MySQL driver allows only one active
	// statement per connection.
	if err := rows.Close(); err != nil {
		return amount, fmt.Errorf("ledger: close credit batches: %w", err)
	}

	left := amount
	for _, b := range batches {
		if left == 0 {
			break
		}
		take := min64(left, b.remaining)
		if take <= 0 {
			continue
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE t_credit_batch
			    SET remaining_micro = remaining_micro - ?,
			        consumed_micro = consumed_micro + ?,
			        updated_at = ?
			  WHERE id = ? AND remaining_micro = ?`,
			take, take, now.UTC(), b.id, b.remaining,
		)
		if err != nil {
			return left, fmt.Errorf("ledger: update credit batch: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return left, fmt.Errorf("ledger: batch rows affected: %w", err)
		}
		if affected != 1 {
			return left, fmt.Errorf("%w: batch_id=%d expected_remaining=%d",
				ErrBatchCAS, b.id, b.remaining)
		}
		out.Debits = append(out.Debits, BatchDebit{
			BatchID:     b.id,
			BatchType:   b.batchType,
			AmountMicro: take,
			AccountType: accountType,
		})
		out.BatchBackedMicro += take
		left -= take
	}
	return left, nil
}

// DebitParams is a strict, non-overdraft debit. Settlement has its own path
// because it also handles the prehold release and truncation; this entry point
// is for manual adjustments and for proving the account CAS under concurrency.
type DebitParams struct {
	UserID      int64
	AmountMicro int64
	RequestID   string
	BizType     string
	IdemPrefix  string
}

// DebitResult reports the batch-level breakdown.
type DebitResult struct {
	DeductedMicro     int64
	BatchBackedMicro  int64
	Debits            []BatchDebit
	BalanceAfterMicro int64
}

// Debit deducts amount from the account without allowing a negative available
// balance. It is atomic: the account CAS and every batch/ledger write share
// one transaction.
func (s *Store) Debit(ctx context.Context, p DebitParams) (DebitResult, error) {
	if p.UserID <= 0 || p.AmountMicro < 0 || p.RequestID == "" {
		return DebitResult{}, fmt.Errorf("%w: user_id/request_id required and amount >= 0", ErrInvalidParam)
	}
	if p.AmountMicro == 0 {
		return DebitResult{}, nil
	}
	bizType := p.BizType
	if bizType == "" {
		bizType = BizTypeConsumeSettle
	}
	idemPrefix := p.IdemPrefix
	if idemPrefix == "" {
		idemPrefix = bizType
	}

	var result DebitResult
	err := s.withTx(ctx, s.DefaultTimeout, func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		acct, err := LockAccount(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if p.AmountMicro > acct.AvailableMicro() {
			return fmt.Errorf("%w: available=%d amount=%d",
				ErrInsufficientCredits, acct.AvailableMicro(), p.AmountMicro)
		}

		out, err := s.deductBatchesTx(ctx, tx, p.UserID, p.AmountMicro, p.AmountMicro, now, s.DeductPolicy)
		if err != nil {
			return err
		}
		if out.BatchBackedMicro != p.AmountMicro {
			return fmt.Errorf("%w: user_id=%d amount=%d backed=%d",
				ErrInsufficientBatches, p.UserID, p.AmountMicro, out.BatchBackedMicro)
		}

		before := acct.TotalMicro()
		bonusDelta, rechargeDelta := int64(0), int64(0)
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
				BizType:             bizType,
				AmountBonusMicro:    bonusAmount,
				AmountRechargeMicro: rechargeAmount,
				AmountTotalMicro:    -d.AmountMicro,
				BalanceBeforeMicro:  before,
				BalanceAfterMicro:   after,
				BatchID:             &batchID,
				RequestID:           &p.RequestID,
				IdemKey:             fmt.Sprintf("%s:%s:%d", idemPrefix, p.RequestID, d.BatchID),
				CreatedAt:           now,
			})
			if err != nil {
				return err
			}
			lastEntryID = entryID
			before = after
		}

		if err := acct.Apply(ctx, tx, BalanceDelta{
			BonusMicro:    bonusDelta,
			RechargeMicro: rechargeDelta,
			ConsumedMicro: p.AmountMicro,
		}, lastEntryID, nil, now); err != nil {
			return err
		}

		result = DebitResult{
			DeductedMicro:     p.AmountMicro,
			BatchBackedMicro:  out.BatchBackedMicro,
			Debits:            out.Debits,
			BalanceAfterMicro: acct.TotalMicro(),
		}
		return nil
	})
	return result, err
}
