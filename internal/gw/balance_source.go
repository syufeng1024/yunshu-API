package gw

import (
	"context"
	"database/sql"
	"errors"
)

// DBAccountSource is the K26 backfill source. It is the ONLY read the gateway
// performs on the main database outside the money transaction: TDS §1 allows a
// mirror miss to load the balance once and refill K26. It is deliberately a
// direct read (no cache, no ledger call) and is used at most once per 60s per
// user.
type DBAccountSource struct {
	DB *sql.DB
}

// LoadBalance implements balance.Source.
func (s DBAccountSource) LoadBalance(ctx context.Context, userID int64) (int64, bool, error) {
	if s.DB == nil {
		return 0, false, errors.New("gw: account source has no database")
	}
	var bonus, recharge, frozen int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT balance_bonus_micro, balance_recharge_micro, frozen_refund_micro
		  FROM t_credit_account
		 WHERE user_id = ?`, userID).Scan(&bonus, &recharge, &frozen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	// Available = the two spendable buckets minus refund-frozen credits
	// (ledger.Account.AvailableMicro uses the same definition).
	return bonus + recharge - frozen, true, nil
}
