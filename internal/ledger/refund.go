package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MinRefundMicro is 1,000 credits = 10 yuan. Refunds below it are rejected
// with the threshold in the message (G-04).
const MinRefundMicro int64 = 1000 * 10000

// RefundQuoteParams asks for the refundable base. RechargeOrderID is optional;
// when present, invoice state and batch source_id are checked for that order.
type RefundQuoteParams struct {
	UserID          int64
	RechargeOrderID *int64
	MinRefundMicro  int64
}

// RefundQuote is the auditable refund base. RefundFen is the 90% cash amount;
// FeeFen is the 10% retained fee. Both are derived from the unspent recharge
// credits only.
type RefundQuote struct {
	RefundableMicro      int64
	GrossFen             int64
	RefundFen            int64
	FeeFen               int64
	Eligible             bool
	Reason               string
	InvoiceState         string
	BatchDetailEstimated bool
}

// RefundFen converts unspent recharge micro-credits to the 90% cash refund in
// fen. 1 credit = 1 fen, so 7,900 credits (79,000,000 micro) => 7,110 fen =
// 71.10 yuan (G-04). The TDS §1.5 printed formula is 100x too large; G-04 is
// the acceptance criterion.
func RefundFen(micro int64) int64 {
	if micro <= 0 {
		return 0
	}
	return micro * 9 / 100000
}

// QuoteRefund computes the refundable base from unspent RECHARGE batches only.
// Bonus batches are never included. An issued invoice blocks the refund; a
// pending invoice requires the user to revoke it first.
func (s *Store) QuoteRefund(ctx context.Context, p RefundQuoteParams) (RefundQuote, error) {
	if p.UserID <= 0 {
		return RefundQuote{}, fmt.Errorf("%w: user_id must be positive", ErrInvalidParam)
	}
	var q RefundQuote
	err := s.withTx(ctx, s.DefaultTimeout, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		q, err = s.quoteRefundTx(ctx, tx, p)
		return err
	})
	return q, err
}

func (s *Store) quoteRefundTx(ctx context.Context, tx *sql.Tx, p RefundQuoteParams) (RefundQuote, error) {
	return s.quoteRefundTxWithFrozen(ctx, tx, p, nil)
}

// QuoteRefundTx is QuoteRefund inside a caller-owned transaction. A caller
// that is about to freeze the quoted amount must lock the account first and
// quote in that same transaction; otherwise a concurrent consumption can move
// the base between the quote and the freeze.
func (s *Store) QuoteRefundTx(ctx context.Context, tx *sql.Tx, p RefundQuoteParams) (RefundQuote, error) {
	if tx == nil {
		return RefundQuote{}, fmt.Errorf("%w: transaction is nil", ErrInvalidParam)
	}
	return s.quoteRefundTx(ctx, tx, p)
}

// quoteRefundTxWithFrozen computes the same quote against an effective frozen
// amount. Refund uses it to release the refund order's own freeze in memory
// before quoting, so the money it is about to pay out is not counted as
// unavailable against itself. A nil override reads the account row.
func (s *Store) quoteRefundTxWithFrozen(ctx context.Context, tx *sql.Tx, p RefundQuoteParams, frozenOverride *int64) (RefundQuote, error) {
	minMicro := p.MinRefundMicro
	if minMicro <= 0 {
		minMicro = MinRefundMicro
	}

	q := RefundQuote{}
	if p.RechargeOrderID != nil {
		err := tx.QueryRowContext(ctx,
			`SELECT state FROM t_invoice_apply
			  WHERE recharge_order_id = ? ORDER BY id DESC LIMIT 1`,
			*p.RechargeOrderID,
		).Scan(&q.InvoiceState)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return q, fmt.Errorf("ledger: read invoice state: %w", err)
		}
	}

	var sum int64
	if p.RechargeOrderID != nil {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(remaining_micro), 0)
			   FROM t_credit_batch
			  WHERE user_id = ? AND refundable = 1 AND expire_at IS NULL
			    AND source_id = ?`,
			p.UserID, *p.RechargeOrderID,
		).Scan(&sum); err != nil {
			return q, fmt.Errorf("ledger: sum refundable batches: %w", err)
		}
	} else {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(remaining_micro), 0)
			   FROM t_credit_batch
			  WHERE user_id = ? AND refundable = 1 AND expire_at IS NULL`,
			p.UserID,
		).Scan(&sum); err != nil {
			return q, fmt.Errorf("ledger: sum refundable batches: %w", err)
		}
	}

	// Money already frozen for a pending refund cannot be quoted again.
	frozen := int64(0)
	if frozenOverride != nil {
		frozen = *frozenOverride
	} else if err := tx.QueryRowContext(ctx,
		`SELECT frozen_refund_micro FROM t_credit_account WHERE user_id = ?`,
		p.UserID,
	).Scan(&frozen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return q, fmt.Errorf("%w: user_id=%d", ErrNoAccount, p.UserID)
		}
		return q, fmt.Errorf("ledger: read frozen refund: %w", err)
	}
	q.RefundableMicro = sum - frozen
	if q.RefundableMicro < 0 {
		q.RefundableMicro = 0
	}

	q.GrossFen = q.RefundableMicro / 10000
	q.RefundFen = RefundFen(q.RefundableMicro)
	q.FeeFen = q.GrossFen - q.RefundFen

	// A consumption without batch_id means the refundable base cannot be
	// recomputed from the ledger; the quote must be flagged for finance review.
	var missing int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_ledger_entry
		  WHERE user_id = ? AND biz_type = ? AND batch_id IS NULL
		    AND (reason IS NULL OR reason <> 'OVERDRAFT_PORTION')`,
		p.UserID, BizTypeConsumeSettle,
	).Scan(&missing); err != nil {
		return q, fmt.Errorf("ledger: count missing batch entries: %w", err)
	}
	q.BatchDetailEstimated = missing > 0

	switch q.InvoiceState {
	case "ISSUED":
		q.Eligible = false
		q.Reason = "已开票订单请联系客服处理"
		return q, nil
	case "PENDING":
		q.Eligible = false
		q.Reason = "开票申请处理中，请先撤销申请后再退款"
		return q, nil
	}
	if q.RefundableMicro < minMicro {
		q.Eligible = false
		q.Reason = fmt.Sprintf("可退积分低于起退额 %d 积分（%d 元）",
			minMicro/10000, minMicro/1000000)
		return q, nil
	}
	q.Eligible = true
	return q, nil
}

// RefundParams performs the refund. RefundOrderID is required: it is the
// idempotency anchor (TDS §5.1) and T13 owns the approval state machine that
// creates the row. This kernel marks it PAID after the money moves.
type RefundParams struct {
	UserID          int64
	RechargeOrderID int64
	RefundOrderID   int64
	MinRefundMicro  int64
}

// RefundResult reports the deducted credits and the untouched bonus balance.
type RefundResult struct {
	RefundOrderID       int64
	RefundableMicro     int64
	RefundFen           int64
	FeeFen              int64
	BalanceAfterMicro   int64
	BonusRemainingMicro int64
	Debits              []BatchDebit
	Idempotent          bool
}

// Refund deducts unspent recharge credits from their batches and writes one
// REFUND DEBIT per batch. Bonus batches are never touched.
func (s *Store) Refund(ctx context.Context, p RefundParams) (RefundResult, error) {
	if p.UserID <= 0 || p.RechargeOrderID <= 0 || p.RefundOrderID <= 0 {
		return RefundResult{}, fmt.Errorf("%w: user_id, recharge_order_id and refund_order_id are required", ErrInvalidParam)
	}
	var result RefundResult
	err := s.withTx(ctx, s.DefaultTimeout, func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		var existing int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM t_ledger_entry
			  WHERE idem_key LIKE CONCAT('refund:', ?, ':%') LIMIT 1`,
			p.RefundOrderID,
		).Scan(&existing)
		if err == nil {
			return s.loadRefundState(ctx, tx, p.UserID, p.RefundOrderID, true, &result)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("ledger: check refund idempotency: %w", err)
		}

		// Lock the refund order before the account. Two concurrent retries for
		// the same refund_order_id serialise here; the loser sees PAID and
		// returns the existing result instead of refunding twice.
		var (
			refundState string
			applyMicro  int64
		)
		err = tx.QueryRowContext(ctx,
			`SELECT state, apply_micro FROM t_refund_order WHERE id = ? FOR UPDATE`,
			p.RefundOrderID,
		).Scan(&refundState, &applyMicro)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: refund_order_id=%d not found", ErrInvalidParam, p.RefundOrderID)
		}
		if err != nil {
			return fmt.Errorf("ledger: lock refund order: %w", err)
		}
		switch refundState {
		case "PAID":
			return s.loadRefundState(ctx, tx, p.UserID, p.RefundOrderID, true, &result)
		case "REJECTED", "FAILED":
			return fmt.Errorf("%w: refund order state %s", ErrRefundIneligible, refundState)
		}

		acct, err := LockAccount(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		// Release this refund order's own freeze before quoting: the frozen
		// amount protects the money from consumption, but must not count as
		// unavailable against the refund that created it (B-43). The release
		// and the balance deduction commit together below.
		release := int64(0)
		if applyMicro > 0 && acct.FrozenRefundMicro > 0 {
			release = min64(applyMicro, acct.FrozenRefundMicro)
		}
		effectiveFrozen := acct.FrozenRefundMicro - release
		quote, err := s.quoteRefundTxWithFrozen(ctx, tx, RefundQuoteParams{
			UserID:          p.UserID,
			RechargeOrderID: &p.RechargeOrderID,
			MinRefundMicro:  p.MinRefundMicro,
		}, &effectiveFrozen)
		if err != nil {
			return err
		}
		if !quote.Eligible {
			return fmt.Errorf("%w: %s", ErrRefundIneligible, quote.Reason)
		}

		refundOrderID := p.RefundOrderID

		before := acct.TotalMicro()
		runBalance := before
		left := quote.RefundableMicro
		lastEntryID := int64(0)

		const batchSQL = `SELECT id, remaining_micro
		                    FROM t_credit_batch
		                   WHERE user_id = ? AND refundable = 1 AND expire_at IS NULL
		                     AND source_id = ? AND remaining_micro > 0
		                   ORDER BY created_at ASC, id ASC
		                   FOR UPDATE`
		rows, err := tx.QueryContext(ctx, batchSQL, p.UserID, p.RechargeOrderID)
		if err != nil {
			return fmt.Errorf("ledger: select refund batches: %w", err)
		}
		type batchRow struct {
			id        int64
			remaining int64
		}
		var batches []batchRow
		for rows.Next() {
			var b batchRow
			if err := rows.Scan(&b.id, &b.remaining); err != nil {
				rows.Close()
				return fmt.Errorf("ledger: scan refund batch: %w", err)
			}
			batches = append(batches, b)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("ledger: iterate refund batches: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("ledger: close refund batches: %w", err)
		}

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
				    SET remaining_micro = remaining_micro - ?, updated_at = ?
				  WHERE id = ? AND remaining_micro = ?`,
				take, now.UTC(), b.id, b.remaining,
			)
			if err != nil {
				return fmt.Errorf("ledger: update refund batch: %w", err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("ledger: refund batch rows: %w", err)
			}
			if affected != 1 {
				return fmt.Errorf("%w: batch_id=%d", ErrBatchCAS, b.id)
			}
			after := runBalance - take
			batchID := b.id
			entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
				UserID:              p.UserID,
				Direction:           DirectionDebit,
				BizType:             BizTypeRefund,
				AmountRechargeMicro: -take,
				AmountTotalMicro:    -take,
				BalanceBeforeMicro:  runBalance,
				BalanceAfterMicro:   after,
				BatchID:             &batchID,
				RefundOrderID:       &refundOrderID,
				IdemKey:             fmt.Sprintf("refund:%d:%d", refundOrderID, b.id),
				CreatedAt:           now,
			})
			if err != nil {
				return err
			}
			lastEntryID = entryID
			runBalance = after
			left -= take
			result.Debits = append(result.Debits, BatchDebit{
				BatchID:     b.id,
				BatchType:   "RECHARGE",
				AmountMicro: take,
				AccountType: AccountTypeRecharge,
			})
		}
		if left > 0 {
			return fmt.Errorf("%w: refund left=%d", ErrInsufficientBatches, left)
		}
		if err := acct.Apply(ctx, tx, BalanceDelta{
			RechargeMicro:     -quote.RefundableMicro,
			FrozenRefundMicro: -release,
		}, lastEntryID, nil, now); err != nil {
			return err
		}

		// Recompute the order's materialized refundable base from its batches.
		var orderRemaining int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(remaining_micro), 0)
			   FROM t_credit_batch
			  WHERE refundable = 1 AND expire_at IS NULL AND source_id = ?`,
			p.RechargeOrderID,
		).Scan(&orderRemaining); err != nil {
			return fmt.Errorf("ledger: recompute order refundable: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE t_recharge_order SET refundable_micro = ?, updated_at = ? WHERE id = ?`,
			orderRemaining, now.UTC(), p.RechargeOrderID,
		); err != nil {
			return fmt.Errorf("ledger: update recharge order: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE t_refund_order
			    SET apply_micro = ?, refund_micro = ?, refund_fen = ?, fee_fen = ?,
			        state = 'PAID', paid_at = ?, updated_at = ?
			  WHERE id = ?`,
			quote.RefundableMicro, quote.RefundableMicro, quote.RefundFen, quote.FeeFen,
			now.UTC(), now.UTC(), refundOrderID,
		); err != nil {
			return fmt.Errorf("ledger: update refund order: %w", err)
		}

		var bonusRemaining int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(remaining_micro), 0)
			   FROM t_credit_batch WHERE user_id = ? AND refundable = 0`,
			p.UserID,
		).Scan(&bonusRemaining); err != nil {
			return fmt.Errorf("ledger: read bonus remaining: %w", err)
		}

		result.RefundOrderID = refundOrderID
		result.RefundableMicro = quote.RefundableMicro
		result.RefundFen = quote.RefundFen
		result.FeeFen = quote.FeeFen
		result.BalanceAfterMicro = acct.TotalMicro()
		result.BonusRemainingMicro = bonusRemaining
		return nil
	})
	return result, err
}

func (s *Store) loadRefundState(
	ctx context.Context,
	tx *sql.Tx,
	userID, refundOrderID int64,
	idempotent bool,
	result *RefundResult,
) error {
	var balance int64
	if err := tx.QueryRowContext(ctx,
		`SELECT balance_micro FROM t_credit_account WHERE user_id = ?`, userID,
	).Scan(&balance); err != nil {
		return fmt.Errorf("ledger: read refund balance: %w", err)
	}
	var refunded int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(-amount_total_micro), 0)
		   FROM t_ledger_entry WHERE refund_order_id = ?`, refundOrderID,
	).Scan(&refunded); err != nil {
		return fmt.Errorf("ledger: sum refund entries: %w", err)
	}
	var bonusRemaining int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(remaining_micro), 0)
		   FROM t_credit_batch WHERE user_id = ? AND refundable = 0`,
		userID,
	).Scan(&bonusRemaining); err != nil {
		return fmt.Errorf("ledger: read bonus remaining: %w", err)
	}
	*result = RefundResult{
		RefundOrderID:       refundOrderID,
		RefundableMicro:     refunded,
		RefundFen:           RefundFen(refunded),
		FeeFen:              refunded/10000 - RefundFen(refunded),
		BalanceAfterMicro:   balance,
		BonusRemainingMicro: bonusRemaining,
		Idempotent:          idempotent,
	}
	return nil
}
