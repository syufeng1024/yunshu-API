package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SettleTxnTimeout is the non-stream settlement budget (TDS §5.1).
const SettleTxnTimeout = 5 * time.Second

// SettleParams is one request settlement. ConsumedMicro is the real cost from
// billing.CostMicro; zero means the request failed and the hold is released in
// full.
type SettleParams struct {
	RequestID     string
	UserID        int64
	ConsumedMicro int64
	ReleaseReason string
}

// SettleResult is the auditable outcome. Debits holds the per-batch DEBIT
// entries; their amounts sum to the batch-backed part of ConsumedMicro.
type SettleResult struct {
	RequestID        string
	State            string
	ConsumedMicro    int64
	PreholdMicro     int64
	ReleasedMicro    int64
	ArrearsMicro     int64
	BatchBackedMicro int64
	OverdraftMicro   int64
	Debits           []BatchDebit
	Idempotent       bool
}

type holdRow struct {
	UserID         int64
	APIKeyID       int64
	HoldMicro      int64
	EstimatedMicro int64
	State          string
	CreatedAt      time.Time
}

// Settle releases the prehold, deducts the real cost batch-by-batch, truncates
// at available + overdraft_limit, writes the excess to t_arrears and freezes
// the user. Repeating the same request_id returns the existing result without
// touching the balance.
func (s *Store) Settle(ctx context.Context, p SettleParams) (SettleResult, error) {
	if p.RequestID == "" || p.ConsumedMicro < 0 {
		return SettleResult{}, fmt.Errorf("%w: request_id required and consumed >= 0", ErrInvalidParam)
	}
	var result SettleResult
	err := s.withTx(ctx, s.SettleTimeout, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		result, err = s.settleTx(ctx, tx, p)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrDuplicateIdem) {
			return s.settledResult(ctx, p.RequestID)
		}
		return SettleResult{}, err
	}
	return result, nil
}

func (s *Store) settleTx(ctx context.Context, tx *sql.Tx, p SettleParams) (SettleResult, error) {
	now := s.now()
	hold, err := s.readHoldRowTx(ctx, tx, p.RequestID)
	if err != nil {
		return SettleResult{}, err
	}
	if p.UserID != 0 && p.UserID != hold.UserID {
		return SettleResult{}, fmt.Errorf("%w: user_id %d does not own request %s",
			ErrInvalidParam, p.UserID, p.RequestID)
	}

	switch hold.State {
	case HoldStateSettled, HoldStateReleased:
		return s.settledResultTx(ctx, tx, p.RequestID, hold)
	case HoldStateSuspended:
		return SettleResult{}, fmt.Errorf("%w: %s", ErrPreholdSuspended, p.RequestID)
	case HoldStateHeld:
		// continue
	default:
		return SettleResult{}, fmt.Errorf("%w: unknown hold state %q", ErrInvalidParam, hold.State)
	}

	// A release without the matching prehold ledger row would create credits
	// from nothing. This is the explicit A8 guard.
	var preholdBonus, preholdRecharge, preholdTotal int64
	err = tx.QueryRowContext(ctx,
		`SELECT amount_bonus_micro, amount_recharge_micro, amount_total_micro
		   FROM t_ledger_entry WHERE idem_key = ? FOR UPDATE`,
		"prehold:"+p.RequestID,
	).Scan(&preholdBonus, &preholdRecharge, &preholdTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return SettleResult{}, fmt.Errorf("%w: request_id=%s", ErrPreholdLedgerMissing, p.RequestID)
	}
	if err != nil {
		return SettleResult{}, fmt.Errorf("ledger: read prehold entry: %w", err)
	}
	if preholdTotal != -hold.HoldMicro {
		return SettleResult{}, fmt.Errorf(
			"%w: ledger_total=%d hold=%d request_id=%s",
			ErrPreholdLedgerMissing, preholdTotal, hold.HoldMicro, p.RequestID)
	}

	acct, err := LockAccount(ctx, tx, hold.UserID)
	if err != nil {
		return SettleResult{}, err
	}
	if preholdBonus > 0 || preholdRecharge > 0 {
		return SettleResult{}, fmt.Errorf("%w: prehold entry must be a debit", ErrInvalidParam)
	}
	bonusCredit := -preholdBonus
	rechargeCredit := -preholdRecharge

	if p.ConsumedMicro == 0 {
		return s.releaseTx(ctx, tx, p, hold, acct, bonusCredit, rechargeCredit, now)
	}
	return s.settleConsumedTx(ctx, tx, p, hold, acct, bonusCredit, rechargeCredit, now)
}

func (s *Store) releaseTx(
	ctx context.Context,
	tx *sql.Tx,
	p SettleParams,
	hold holdRow,
	acct *Account,
	bonusCredit, rechargeCredit int64,
	now time.Time,
) (SettleResult, error) {
	before := acct.TotalMicro()
	requestID := p.RequestID
	entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
		UserID:              hold.UserID,
		Direction:           DirectionCredit,
		BizType:             BizTypePreholdRelease,
		AmountBonusMicro:    bonusCredit,
		AmountRechargeMicro: rechargeCredit,
		AmountTotalMicro:    hold.HoldMicro,
		BalanceBeforeMicro:  before,
		BalanceAfterMicro:   before + hold.HoldMicro,
		RequestID:           &requestID,
		IdemKey:             "release:" + p.RequestID,
		Reason:              reasonPtr(p.ReleaseReason),
		CreatedAt:           now,
	})
	if err != nil {
		return SettleResult{}, err
	}
	if err := acct.Apply(ctx, tx, BalanceDelta{
		BonusMicro:    bonusCredit,
		RechargeMicro: rechargeCredit,
	}, entryID, nil, now); err != nil {
		return SettleResult{}, err
	}
	if err := markHold(ctx, tx, p.RequestID, HoldStateReleased, now, p.ReleaseReason); err != nil {
		return SettleResult{}, err
	}
	return SettleResult{
		RequestID:     p.RequestID,
		State:         HoldStateReleased,
		ConsumedMicro: 0,
		PreholdMicro:  hold.HoldMicro,
		ReleasedMicro: hold.HoldMicro,
	}, nil
}

func (s *Store) settleConsumedTx(
	ctx context.Context,
	tx *sql.Tx,
	p SettleParams,
	hold holdRow,
	acct *Account,
	bonusCredit, rechargeCredit int64,
	now time.Time,
) (SettleResult, error) {
	before := acct.TotalMicro()
	afterRelease := before + hold.HoldMicro
	availableAfterRelease := acct.AvailableMicro() + hold.HoldMicro

	// Settlement-side hard cap: available balance + overdraft limit. The
	// overdraft limit is never added to the admission balance.
	capMicro := availableAfterRelease + acct.OverdraftLimitMicro
	if capMicro < 0 {
		capMicro = 0
	}
	deductTotal := p.ConsumedMicro
	if deductTotal > capMicro {
		deductTotal = capMicro
	}
	arrearsMicro := p.ConsumedMicro - deductTotal

	batchCap := availableAfterRelease
	if batchCap < 0 {
		batchCap = 0
	}
	if batchCap > deductTotal {
		batchCap = deductTotal
	}

	requestID := p.RequestID
	lastEntryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
		UserID:              hold.UserID,
		Direction:           DirectionCredit,
		BizType:             BizTypePreholdRelease,
		AmountBonusMicro:    bonusCredit,
		AmountRechargeMicro: rechargeCredit,
		AmountTotalMicro:    hold.HoldMicro,
		BalanceBeforeMicro:  before,
		BalanceAfterMicro:   afterRelease,
		RequestID:           &requestID,
		IdemKey:             "release:" + p.RequestID,
		CreatedAt:           now,
	})
	if err != nil {
		return SettleResult{}, err
	}

	out, err := s.deductBatchesTx(ctx, tx, hold.UserID, deductTotal, batchCap, now, s.DeductPolicy)
	if err != nil {
		return SettleResult{}, err
	}
	if out.BatchBackedMicro != batchCap {
		return SettleResult{}, fmt.Errorf(
			"%w: user_id=%d batch_cap=%d backed=%d request_id=%s",
			ErrInsufficientBatches, hold.UserID, batchCap, out.BatchBackedMicro, p.RequestID)
	}

	runBalance := afterRelease
	curBonus := acct.BalanceBonusMicro + bonusCredit
	curRecharge := acct.BalanceRechargeMicro + rechargeCredit
	bonusDelta := bonusCredit
	rechargeDelta := rechargeCredit

	settleEntryIndex := 0
	for _, d := range out.Debits {
		bonusAmount, rechargeAmount := int64(0), int64(0)
		if d.AccountType == AccountTypeBonus {
			bonusAmount = -d.AmountMicro
			bonusDelta -= d.AmountMicro
			curBonus -= d.AmountMicro
		} else {
			rechargeAmount = -d.AmountMicro
			rechargeDelta -= d.AmountMicro
			curRecharge -= d.AmountMicro
		}
		after := runBalance - d.AmountMicro
		batchID := d.BatchID
		// The first settlement row carries the request-level idem key
		// settle:{request_id}; later batch rows add the batch id. The unique
		// key on the first row is the final guard against a double settlement.
		idem := fmt.Sprintf("settle:%s:%d", p.RequestID, d.BatchID)
		if settleEntryIndex == 0 {
			idem = "settle:" + p.RequestID
		}
		settleEntryIndex++
		entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
			UserID:              hold.UserID,
			Direction:           DirectionDebit,
			BizType:             BizTypeConsumeSettle,
			AmountBonusMicro:    bonusAmount,
			AmountRechargeMicro: rechargeAmount,
			AmountTotalMicro:    -d.AmountMicro,
			BalanceBeforeMicro:  runBalance,
			BalanceAfterMicro:   after,
			BatchID:             &batchID,
			RequestID:           &requestID,
			IdemKey:             idem,
			CreatedAt:           now,
		})
		if err != nil {
			return SettleResult{}, err
		}
		lastEntryID = entryID
		runBalance = after
	}

	overdraftPart := deductTotal - out.BatchBackedMicro
	if overdraftPart > 0 {
		bonusPart := int64(0)
		if curBonus > 0 {
			bonusPart = min64(overdraftPart, curBonus)
		}
		rechargePart := overdraftPart - bonusPart
		after := runBalance - overdraftPart
		reason := "OVERDRAFT_PORTION"
		idem := fmt.Sprintf("settle:%s:overdraft", p.RequestID)
		if settleEntryIndex == 0 {
			idem = "settle:" + p.RequestID
		}
		entryID, err := AppendEntry(ctx, tx, s.Entries, now, Entry{
			UserID:              hold.UserID,
			Direction:           DirectionDebit,
			BizType:             BizTypeConsumeSettle,
			AmountBonusMicro:    -bonusPart,
			AmountRechargeMicro: -rechargePart,
			AmountTotalMicro:    -overdraftPart,
			BalanceBeforeMicro:  runBalance,
			BalanceAfterMicro:   after,
			RequestID:           &requestID,
			IdemKey:             idem,
			Reason:              &reason,
			CreatedAt:           now,
		})
		if err != nil {
			return SettleResult{}, err
		}
		lastEntryID = entryID
		bonusDelta -= bonusPart
		rechargeDelta -= rechargePart
		curBonus -= bonusPart
		curRecharge -= rechargePart
		runBalance = after
	}

	if runBalance != afterRelease-deductTotal {
		return SettleResult{}, fmt.Errorf(
			"ledger: settlement balance drift: run=%d want=%d",
			runBalance, afterRelease-deductTotal)
	}

	settledAt := now
	if err := acct.Apply(ctx, tx, BalanceDelta{
		BonusMicro:    bonusDelta,
		RechargeMicro: rechargeDelta,
		ConsumedMicro: deductTotal,
	}, lastEntryID, &settledAt, now); err != nil {
		return SettleResult{}, err
	}

	if err := markHold(ctx, tx, p.RequestID, HoldStateSettled, now, ""); err != nil {
		return SettleResult{}, err
	}

	if arrearsMicro > 0 {
		if _, err := insertArrearsTx(ctx, tx, s.Entries, now, hold.UserID, p.RequestID,
			arrearsMicro, acct.OverdraftLimitMicro); err != nil {
			return SettleResult{}, err
		}
	}

	return SettleResult{
		RequestID:        p.RequestID,
		State:            HoldStateSettled,
		ConsumedMicro:    deductTotal,
		PreholdMicro:     hold.HoldMicro,
		ReleasedMicro:    hold.HoldMicro - deductTotal,
		ArrearsMicro:     arrearsMicro,
		BatchBackedMicro: out.BatchBackedMicro,
		OverdraftMicro:   overdraftPart,
		Debits:           out.Debits,
	}, nil
}

func (s *Store) readHoldRowTx(ctx context.Context, tx *sql.Tx, requestID string) (holdRow, error) {
	const q = `SELECT user_id, api_key_id, hold_micro, estimated_micro, state, created_at
	             FROM t_prehold_hold WHERE request_id = ? FOR UPDATE`
	var h holdRow
	err := tx.QueryRowContext(ctx, q, requestID).Scan(
		&h.UserID, &h.APIKeyID, &h.HoldMicro, &h.EstimatedMicro, &h.State, &h.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return holdRow{}, fmt.Errorf("%w: %s", ErrPreholdNotFound, requestID)
	}
	if err != nil {
		return holdRow{}, fmt.Errorf("ledger: read prehold: %w", err)
	}
	return h, nil
}

func (s *Store) settledResult(ctx context.Context, requestID string) (SettleResult, error) {
	var result SettleResult
	err := s.withTx(ctx, s.SettleTimeout, func(ctx context.Context, tx *sql.Tx) error {
		hold, err := s.readHoldRowTx(ctx, tx, requestID)
		if err != nil {
			return err
		}
		result, err = s.settledResultTx(ctx, tx, requestID, hold)
		return err
	})
	return result, err
}

func (s *Store) settledResultTx(ctx context.Context, tx *sql.Tx, requestID string, hold holdRow) (SettleResult, error) {
	var sum int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_total_micro), 0)
		   FROM t_ledger_entry
		  WHERE request_id = ? AND biz_type = ?`,
		requestID, BizTypeConsumeSettle,
	).Scan(&sum); err != nil {
		return SettleResult{}, fmt.Errorf("ledger: sum settlement entries: %w", err)
	}
	consumed := -sum
	if consumed < 0 {
		consumed = 0
	}
	var arrears int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_micro), 0) FROM t_arrears WHERE request_id = ?`,
		requestID,
	).Scan(&arrears); err != nil {
		return SettleResult{}, fmt.Errorf("ledger: sum arrears: %w", err)
	}
	return SettleResult{
		RequestID:     requestID,
		State:         hold.State,
		ConsumedMicro: consumed,
		PreholdMicro:  hold.HoldMicro,
		ReleasedMicro: hold.HoldMicro - consumed,
		ArrearsMicro:  arrears,
		Idempotent:    true,
	}, nil
}

func markHold(ctx context.Context, tx *sql.Tx, requestID, state string, now time.Time, reason string) error {
	var res sql.Result
	var err error
	switch state {
	case HoldStateReleased:
		var reasonArg any
		if reason != "" {
			reasonArg = reason
		}
		res, err = tx.ExecContext(ctx,
			`UPDATE t_prehold_hold
			    SET state = ?, released_at = ?, release_reason = ?
			  WHERE request_id = ? AND state = ?`,
			state, now.UTC(), reasonArg, requestID, HoldStateHeld,
		)
	case HoldStateSettled:
		res, err = tx.ExecContext(ctx,
			`UPDATE t_prehold_hold
			    SET state = ?, settled_at = ?
			  WHERE request_id = ? AND state = ?`,
			state, now.UTC(), requestID, HoldStateHeld,
		)
	default:
		return fmt.Errorf("%w: unsupported hold transition %q", ErrInvalidParam, state)
	}
	if err != nil {
		return fmt.Errorf("ledger: update prehold state: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ledger: prehold rows affected: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: request_id=%s transition to %s", ErrCASConflict, requestID, state)
	}
	return nil
}

func reasonPtr(reason string) *string {
	if reason == "" {
		return nil
	}
	return &reason
}
