package recon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/platform/clock"
)

// ReversalReasonPrefix is the structured prefix required by TDS §8.3.5.
const ReversalReasonPrefix = "REVERSAL_OF:"

// ReverseParams is one full reversal. The reason is the operator's text and
// must be at least 10 Chinese characters; the prefix is added by the service.
type ReverseParams struct {
	EntryNo    string
	Reason     string
	OperatorID int64
}

// ReverseResult is the appended reversal. Idempotent is true when the unique
// idem key already existed and nothing was written.
type ReverseResult struct {
	ID               int64
	EntryNo          string
	OriginalEntryNo  string
	AmountTotalMicro int64
	BalanceBefore    int64
	BalanceAfter     int64
	Idempotent       bool
}

// Reverser appends reversal entries.
//
// append-only：本类型只 INSERT t_ledger_entry，不 UPDATE、不 DELETE 历史行。
// t_ledger_entry 没有 reversal_of 列（TDS §8.3.5），原流水通过
// idem_key='reversal:'+原 entry_no 与 reason 前缀交叉引用。
type Reverser struct {
	DB    *sql.DB
	Store *ledger.Store
	Clock clock.Clock
}

// NewReverser builds the service.
func NewReverser(db *sql.DB, clk clock.Clock) *Reverser {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Reverser{DB: db, Store: ledger.NewStore(db, clk), Clock: clk}
}

type originalEntry struct {
	ID            int64
	UserID        int64
	EntryNo       string
	Direction     string
	Bonus         int64
	Recharge      int64
	Total         int64
	BalanceBefore int64
	BalanceAfter  int64
	IdemKey       string
}

// Reverse appends one full reversal and then re-runs the three post-reversal
// assertions. A verification failure is returned with the committed result:
// the correct reaction is human review, not a second reversal.
func (r *Reverser) Reverse(ctx context.Context, p ReverseParams) (ReverseResult, error) {
	if r == nil || r.DB == nil || r.Store == nil {
		return ReverseResult{}, fmt.Errorf("recon: reversal requires a database")
	}
	p.EntryNo = strings.TrimSpace(p.EntryNo)
	if p.EntryNo == "" {
		return ReverseResult{}, fmt.Errorf("recon: reversal requires the original entry_no")
	}
	if utf8.RuneCountInString(strings.TrimSpace(p.Reason)) < 10 {
		return ReverseResult{}, fmt.Errorf("recon: reversal reason must be at least 10 characters")
	}
	if p.OperatorID <= 0 {
		return ReverseResult{}, fmt.Errorf("recon: reversal requires an operator_id")
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReverseResult{}, fmt.Errorf("recon: begin reversal: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // commit below decides the outcome
	var result ReverseResult
	result, err = r.reverseTx(ctx, tx, p)
	if err == nil {
		err = tx.Commit()
	}
	if errors.Is(err, ledger.ErrDuplicateIdem) {
		return r.existing(ctx, p.EntryNo)
	}
	if err != nil {
		return ReverseResult{}, err
	}
	if err := r.Verify(ctx, result.OriginalEntryNo); err != nil {
		return result, fmt.Errorf("recon: reversal committed but post-check failed: %w", err)
	}
	return result, nil
}

func (r *Reverser) reverseTx(ctx context.Context, tx *sql.Tx, p ReverseParams) (ReverseResult, error) {
	orig, err := readOriginal(ctx, tx, p.EntryNo)
	if err != nil {
		return ReverseResult{}, err
	}
	if orig.Total == 0 {
		return ReverseResult{}, fmt.Errorf("recon: entry %s has zero amount and cannot be reversed", p.EntryNo)
	}
	acct, err := ledger.LockAccount(ctx, tx, orig.UserID)
	if err != nil {
		return ReverseResult{}, err
	}
	before := acct.TotalMicro()
	bonus := -orig.Bonus
	recharge := -orig.Recharge
	total := -orig.Total
	after := before + total

	direction := ledger.DirectionCredit
	if total < 0 {
		direction = ledger.DirectionDebit
	}
	reason := TruncateRunes(ReversalReasonPrefix+orig.EntryNo+":"+strings.TrimSpace(p.Reason), 255)
	now := r.now()
	entryID, err := ledger.AppendEntry(ctx, tx, r.Store.Entries, now, ledger.Entry{
		UserID:              orig.UserID,
		Direction:           direction,
		BizType:             ledger.BizTypeAdjust,
		AmountBonusMicro:    bonus,
		AmountRechargeMicro: recharge,
		AmountTotalMicro:    total,
		BalanceBeforeMicro:  before,
		BalanceAfterMicro:   after,
		IdemKey:             "reversal:" + orig.EntryNo,
		OperatorID:          &p.OperatorID,
		Reason:              &reason,
		CreatedAt:           now,
	})
	if err != nil {
		return ReverseResult{}, err
	}
	if err := acct.Apply(ctx, tx, ledger.BalanceDelta{
		BonusMicro: bonus, RechargeMicro: recharge,
	}, entryID, nil, now); err != nil {
		return ReverseResult{}, err
	}
	entryNo, err := r.entryNoByID(ctx, tx, entryID)
	if err != nil {
		return ReverseResult{}, err
	}
	return ReverseResult{
		ID: entryID, EntryNo: entryNo, OriginalEntryNo: orig.EntryNo,
		AmountTotalMicro: total, BalanceBefore: before, BalanceAfter: after,
	}, nil
}

func readOriginal(ctx context.Context, tx *sql.Tx, entryNo string) (originalEntry, error) {
	var o originalEntry
	err := tx.QueryRowContext(ctx,
		`SELECT id, user_id, entry_no, direction, amount_bonus_micro,
		        amount_recharge_micro, amount_total_micro,
		        balance_before_micro, balance_after_micro, idem_key
		   FROM t_ledger_entry WHERE entry_no = ?`, entryNo,
	).Scan(&o.ID, &o.UserID, &o.EntryNo, &o.Direction, &o.Bonus,
		&o.Recharge, &o.Total, &o.BalanceBefore, &o.BalanceAfter, &o.IdemKey)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("recon: original entry %s not found", entryNo)
	}
	if err != nil {
		return o, fmt.Errorf("recon: read original entry %s: %w", entryNo, err)
	}
	return o, nil
}

func (r *Reverser) entryNoByID(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var no string
	if err := tx.QueryRowContext(ctx,
		`SELECT entry_no FROM t_ledger_entry WHERE id = ?`, id).Scan(&no); err != nil {
		return "", fmt.Errorf("recon: read reversal entry_no: %w", err)
	}
	return no, nil
}

// existing returns the reversal already recorded for the original. It is the
// idempotent branch: a second call is a success with no new row.
func (r *Reverser) existing(ctx context.Context, originalEntryNo string) (ReverseResult, error) {
	var out ReverseResult
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, entry_no, amount_total_micro, balance_before_micro, balance_after_micro
		   FROM t_ledger_entry WHERE idem_key = ?`, "reversal:"+originalEntryNo,
	).Scan(&out.ID, &out.EntryNo, &out.AmountTotalMicro, &out.BalanceBefore, &out.BalanceAfter)
	if err != nil {
		return ReverseResult{}, fmt.Errorf("recon: read existing reversal: %w", err)
	}
	out.OriginalEntryNo = originalEntryNo
	out.Idempotent = true
	return out, nil
}

// Verify re-runs the three assertions from TDS §8.3.5 for one user. It is
// read-only and returns an error for the first failed assertion.
func (r *Reverser) Verify(ctx context.Context, entryNo string) error {
	var userID int64
	if err := r.DB.QueryRowContext(ctx,
		`SELECT user_id FROM t_ledger_entry WHERE entry_no = ?`, entryNo).Scan(&userID); err != nil {
		return fmt.Errorf("recon: verify read entry: %w", err)
	}

	// ① balance == ledger sum
	var balance, sum int64
	err := r.DB.QueryRowContext(ctx,
		`SELECT a.balance_micro, COALESCE(SUM(l.amount_total_micro), 0)
		   FROM t_credit_account a
		   LEFT JOIN t_ledger_entry l ON l.user_id = a.user_id
		  WHERE a.user_id = ?
		  GROUP BY a.user_id, a.balance_micro`, userID,
	).Scan(&balance, &sum)
	if err != nil {
		return fmt.Errorf("recon: verify R-01 user=%d: %w", userID, err)
	}
	if balance != sum {
		return fmt.Errorf("recon: verify R-01 user=%d balance=%d ledger_sum=%d", userID, balance, sum)
	}

	// ② continuity
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, balance_before_micro, balance_after_micro
		   FROM t_ledger_entry WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return fmt.Errorf("recon: verify R-03 user=%d: %w", userID, err)
	}
	defer rows.Close()
	var prevAfter int64
	have := false
	for rows.Next() {
		var id, before, after int64
		if err := rows.Scan(&id, &before, &after); err != nil {
			return fmt.Errorf("recon: verify R-03 scan: %w", err)
		}
		if have && before != prevAfter {
			return fmt.Errorf("recon: verify R-03 user=%d entry=%d before=%d prev_after=%d",
				userID, id, before, prevAfter)
		}
		prevAfter, have = after, true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("recon: verify R-03 rows: %w", err)
	}

	// ③ the reversal is self-consistent and negates the original.
	var revTotal, revDelta, origTotal int64
	err = r.DB.QueryRowContext(ctx,
		`SELECT rev.amount_total_micro,
		        rev.balance_after_micro - rev.balance_before_micro,
		        orig.amount_total_micro
		   FROM t_ledger_entry rev
		   JOIN t_ledger_entry orig ON orig.entry_no = ?
		  WHERE rev.idem_key = ?`,
		entryNo, "reversal:"+entryNo,
	).Scan(&revTotal, &revDelta, &origTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("recon: verify reversal for %s not found", entryNo)
	}
	if err != nil {
		return fmt.Errorf("recon: verify reversal: %w", err)
	}
	if revTotal != -origTotal || revDelta != revTotal {
		return fmt.Errorf("recon: verify reversal %s total=%d delta=%d original=%d",
			entryNo, revTotal, revDelta, origTotal)
	}
	return nil
}

func (r *Reverser) now() time.Time {
	if r == nil || r.Clock == nil {
		return time.Now().UTC()
	}
	return r.Clock.Now().UTC()
}

// TruncateRunes cuts to at most n runes without splitting a UTF-8 sequence.
func TruncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
