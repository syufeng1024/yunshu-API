package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Arrears states.
const (
	ArrearsStateOpen    = "OPEN"
	ArrearsStatePartial = "PARTIAL"
	ArrearsStateRepaid  = "REPAID"
)

// ArrearsOffset is one repaid arrears row.
type ArrearsOffset struct {
	ID          int64
	ArrearsNo   string
	AmountMicro int64
	State       string
}

// insertArrearsTx records the part of a request cost that the account cannot
// absorb. The excess never enters the balance; it lives only in t_arrears, so
// balance + arrears = real cost. The account is frozen immediately.
func insertArrearsTx(
	ctx context.Context,
	tx *sql.Tx,
	gen *EntryNoGenerator,
	now time.Time,
	userID int64,
	requestID string,
	amountMicro, overdraftLimitMicro int64,
) (int64, error) {
	if amountMicro <= 0 {
		return 0, nil
	}
	if requestID == "" {
		return 0, fmt.Errorf("%w: request_id is required for arrears", ErrInvalidParam)
	}
	const q = `INSERT INTO t_arrears (
		user_id, request_id, arrears_no, amount_micro, settled_micro, state,
		overdraft_limit_micro, freeze_flag, created_at, updated_at
	) VALUES (?,?,?,?,0,?,?,1,?,?)
	ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`
	res, err := tx.ExecContext(ctx, q,
		userID, requestID, gen.Next(now), amountMicro, ArrearsStateOpen,
		overdraftLimitMicro, now.UTC(), now.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("ledger: insert arrears: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("ledger: arrears id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE t_user SET status = ?, updated_at = ?
		  WHERE id = ? AND status = ?`,
		UserStatusFrozenOverdraft, now.UTC(), userID, UserStatusNormal,
	); err != nil {
		return 0, fmt.Errorf("ledger: freeze user: %w", err)
	}
	return id, nil
}

// offsetArrearsTx repays open/partial arrears first. The caller is responsible
// for the matching ledger entries and batch decrement: the recharge credit and
// the OVERDRAFT_OFFSET debit must be in the same transaction.
func offsetArrearsTx(
	ctx context.Context,
	tx *sql.Tx,
	userID, amount int64,
	now time.Time,
) (int64, []ArrearsOffset, error) {
	if amount <= 0 {
		return 0, nil, nil
	}
	const q = `SELECT id, arrears_no, amount_micro, settled_micro, state
	             FROM t_arrears
	            WHERE user_id = ? AND state IN (?, ?)
	            ORDER BY created_at ASC, id ASC
	            FOR UPDATE`
	rows, err := tx.QueryContext(ctx, q, userID, ArrearsStateOpen, ArrearsStatePartial)
	if err != nil {
		return 0, nil, fmt.Errorf("ledger: select arrears: %w", err)
	}
	type row struct {
		id      int64
		no      string
		amount  int64
		settled int64
		state   string
	}
	var open []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.no, &r.amount, &r.settled, &r.state); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("ledger: scan arrears: %w", err)
		}
		open = append(open, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("ledger: iterate arrears: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("ledger: close arrears: %w", err)
	}

	left := amount
	var offsets []ArrearsOffset
	for _, r := range open {
		if left == 0 {
			break
		}
		due := r.amount - r.settled
		if due <= 0 {
			continue
		}
		pay := min64(due, left)
		newSettled := r.settled + pay
		state := ArrearsStatePartial
		if newSettled >= r.amount {
			state = ArrearsStateRepaid
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE t_arrears SET settled_micro = ?, state = ?, updated_at = ? WHERE id = ?`,
			newSettled, state, now.UTC(), r.id,
		); err != nil {
			return 0, nil, fmt.Errorf("ledger: update arrears: %w", err)
		}
		offsets = append(offsets, ArrearsOffset{
			ID:          r.id,
			ArrearsNo:   r.no,
			AmountMicro: pay,
			State:       state,
		})
		left -= pay
	}

	offsetTotal := amount - left
	if offsetTotal > 0 {
		var stillOpen int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM t_arrears WHERE user_id = ? AND state IN (?, ?)`,
			userID, ArrearsStateOpen, ArrearsStatePartial,
		).Scan(&stillOpen); err != nil {
			return 0, nil, fmt.Errorf("ledger: count open arrears: %w", err)
		}
		if stillOpen == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE t_user SET status = ?, updated_at = ?
				  WHERE id = ? AND status = ?`,
				UserStatusNormal, now.UTC(), userID, UserStatusFrozenOverdraft,
			); err != nil {
				return 0, nil, fmt.Errorf("ledger: unfreeze user: %w", err)
			}
		}
	}
	return offsetTotal, offsets, nil
}

// RechargeParams credits a paid recharge and immediately offsets open arrears.
// The recharge batch is refundable (expire_at NULL); the amount used for the
// offset is removed from that same batch, so the batch and account agree.
type RechargeParams struct {
	UserID          int64
	AmountMicro     int64
	RechargeOrderID int64
	BatchNo         string
	IdemKey         string
	SourceID        *int64
}

// RechargeResult reports the split between debt repayment and new balance.
type RechargeResult struct {
	RechargeMicro         int64
	OffsetMicro           int64
	RepairMicro           int64
	RemainingArrearsMicro int64
	BalanceAfterMicro     int64
	UserStatus            string
	BatchID               int64
	Idempotent            bool
}

// Recharge credits the account, then repays arrears first, then repairs any
// negative balance, and only then leaves a positive available balance. The
// order is enforced by applying the full credit and then debiting the offset:
// net balance = before + amount - offset.
func (s *Store) Recharge(ctx context.Context, p RechargeParams) (RechargeResult, error) {
	if p.UserID <= 0 || p.AmountMicro <= 0 {
		return RechargeResult{}, fmt.Errorf("%w: user_id and amount must be positive", ErrInvalidParam)
	}
	idem := p.IdemKey
	if idem == "" {
		if p.RechargeOrderID <= 0 {
			return RechargeResult{}, fmt.Errorf("%w: idem_key or recharge_order_id is required", ErrInvalidParam)
		}
		idem = fmt.Sprintf("recharge:%d", p.RechargeOrderID)
	}

	var result RechargeResult
	err := s.withTx(ctx, s.DefaultTimeout, func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		var existing int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM t_ledger_entry WHERE idem_key = ?`, idem,
		).Scan(&existing)
		if err == nil {
			return s.loadRechargeState(ctx, tx, p.UserID, p.AmountMicro, &result, true)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("ledger: check recharge idempotency: %w", err)
		}

		acct, err := LockAccount(ctx, tx, p.UserID)
		if err != nil {
			return err
		}

		batchID, err := s.insertRechargeBatch(ctx, tx, p, now)
		if err != nil {
			return err
		}

		beforeTotal := acct.TotalMicro()
		var orderID *int64
		if p.RechargeOrderID > 0 {
			orderID = &p.RechargeOrderID
		}
		entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
			UserID:              p.UserID,
			Direction:           DirectionCredit,
			BizType:             BizTypeRecharge,
			AmountRechargeMicro: p.AmountMicro,
			AmountTotalMicro:    p.AmountMicro,
			BalanceBeforeMicro:  beforeTotal,
			BalanceAfterMicro:   beforeTotal + p.AmountMicro,
			RechargeOrderID:     orderID,
			IdemKey:             idem,
			CreatedAt:           now,
		})
		if err != nil {
			return err
		}
		if err := acct.Apply(ctx, tx, BalanceDelta{
			RechargeMicro:      p.AmountMicro,
			TotalRechargeMicro: p.AmountMicro,
		}, entryID, nil, now); err != nil {
			return err
		}

		offset, offsets, err := offsetArrearsTx(ctx, tx, p.UserID, p.AmountMicro, now)
		if err != nil {
			return err
		}

		lastEntryID := entryID
		runBalance := acct.TotalMicro()
		for _, o := range offsets {
			after := runBalance - o.AmountMicro
			batchRef := batchID
			reason := "arrears:" + o.ArrearsNo
			offsetID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
				UserID:              p.UserID,
				Direction:           DirectionDebit,
				BizType:             BizTypeOverdraftOffset,
				AmountRechargeMicro: -o.AmountMicro,
				AmountTotalMicro:    -o.AmountMicro,
				BalanceBeforeMicro:  runBalance,
				BalanceAfterMicro:   after,
				BatchID:             &batchRef,
				IdemKey:             fmt.Sprintf("offset:%s:%d", idem, o.ID),
				Reason:              &reason,
				CreatedAt:           now,
			})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE t_credit_batch
				    SET remaining_micro = remaining_micro - ?,
				        consumed_micro = consumed_micro + ?,
				        updated_at = ?
				  WHERE id = ? AND remaining_micro >= ?`,
				o.AmountMicro, o.AmountMicro, now.UTC(), batchID, o.AmountMicro,
			); err != nil {
				return fmt.Errorf("ledger: offset batch: %w", err)
			}
			runBalance = after
			lastEntryID = offsetID
		}
		if offset > 0 {
			if err := acct.Apply(ctx, tx, BalanceDelta{
				RechargeMicro: -offset,
			}, lastEntryID, nil, now); err != nil {
				return err
			}
		}

		// A negative balance has no batch backing: all older batches are
		// already exhausted. The part of the recharge that repairs that
		// deficit is not spendable and must also leave the new batch, or the
		// batch total would exceed max(account balance, 0). Example: -500,000
		// balance, +2,000,000 recharge, 1,100,000 arrears => 400,000 batch
		// remaining, not 900,000.
		repair := int64(0)
		if beforeTotal < 0 {
			leftAfterOffset := p.AmountMicro - offset
			repair = min64(-beforeTotal, leftAfterOffset)
		}
		if repair > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE t_credit_batch
				    SET remaining_micro = remaining_micro - ?,
				        consumed_micro = consumed_micro + ?,
				        updated_at = ?
				  WHERE id = ? AND remaining_micro >= ?`,
				repair, repair, now.UTC(), batchID, repair,
			); err != nil {
				return fmt.Errorf("ledger: repair negative balance batch: %w", err)
			}
		}
		if err := s.loadRechargeState(ctx, tx, p.UserID, p.AmountMicro, &result, false); err != nil {
			return err
		}
		result.BatchID = batchID
		result.OffsetMicro = offset
		result.RepairMicro = repair
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrDuplicateIdem) {
			return s.loadRechargeStateOutside(ctx, p.UserID, p.AmountMicro)
		}
		return RechargeResult{}, err
	}
	return result, nil
}

func (s *Store) insertRechargeBatch(ctx context.Context, tx *sql.Tx, p RechargeParams, now time.Time) (int64, error) {
	sourceID := p.SourceID
	if sourceID == nil && p.RechargeOrderID > 0 {
		sourceID = &p.RechargeOrderID
	}
	const q = `INSERT INTO t_credit_batch (
		user_id, batch_no, batch_type, source_id, face_value_micro,
		remaining_micro, consumed_micro, refundable, effective_at, expire_at,
		created_at, updated_at
	) VALUES (?,?,?,?,?,?,0,1,?,NULL,?,?)`
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		batchNo := p.BatchNo
		if batchNo == "" {
			batchNo = now.UTC().Format("20060102") + randomDigits(6)
		}
		res, err := tx.ExecContext(ctx, q,
			p.UserID, batchNo, "RECHARGE", sourceID, p.AmountMicro,
			p.AmountMicro, now.UTC(), now.UTC(), now.UTC(),
		)
		if err == nil {
			return res.LastInsertId()
		}
		if !isDuplicate(err) || p.BatchNo != "" {
			return 0, fmt.Errorf("ledger: insert recharge batch: %w", err)
		}
		lastErr = err
	}
	return 0, fmt.Errorf("ledger: insert recharge batch: %w", lastErr)
}

func (s *Store) loadRechargeState(
	ctx context.Context,
	tx *sql.Tx,
	userID, amount int64,
	result *RechargeResult,
	idempotent bool,
) error {
	var balance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT balance_micro FROM t_credit_account WHERE user_id = ?`, userID,
	).Scan(&balance); err != nil {
		return fmt.Errorf("ledger: read recharge balance: %w", err)
	}
	var remaining int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_micro - settled_micro), 0)
		   FROM t_arrears WHERE user_id = ? AND state IN (?, ?)`,
		userID, ArrearsStateOpen, ArrearsStatePartial,
	).Scan(&remaining); err != nil {
		return fmt.Errorf("ledger: read remaining arrears: %w", err)
	}
	var status string
	if err := tx.QueryRowContext(ctx,
		`SELECT status FROM t_user WHERE id = ?`, userID,
	).Scan(&status); err != nil {
		return fmt.Errorf("ledger: read user status: %w", err)
	}
	*result = RechargeResult{
		RechargeMicro:         amount,
		RemainingArrearsMicro: remaining,
		BalanceAfterMicro:     balance,
		UserStatus:            status,
		Idempotent:            idempotent,
	}
	return nil
}

func (s *Store) loadRechargeStateOutside(ctx context.Context, userID, amount int64) (RechargeResult, error) {
	var result RechargeResult
	err := s.withTx(ctx, s.DefaultTimeout, func(ctx context.Context, tx *sql.Tx) error {
		return s.loadRechargeState(ctx, tx, userID, amount, &result, true)
	})
	return result, err
}
