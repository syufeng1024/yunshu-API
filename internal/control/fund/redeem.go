package fund

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/ledger"
)

// Redeem code states (t_redeem_code.status).
const (
	redeemUnused  = "UNUSED"
	redeemUsed    = "USED"
	redeemExpired = "EXPIRED"
	redeemVoid    = "VOID"
)

type redeemRequest struct {
	Code string `json:"code"`
}

type redeemResponse struct {
	BatchName         string    `json:"batch_name"`
	AmountMicro       int64     `json:"amount_micro"`
	Amount            int64     `json:"amount"`
	CreditTarget      string    `json:"credit_target"`
	ExpireAt          time.Time `json:"expire_at"`
	BalanceAfterMicro int64     `json:"balance_after_micro"`
}

// Redeem implements POST /api/console/redeem (F10-3 / B-44).
//
// A code is single-use and belongs to exactly one batch. Redemption credits
// the account through the ledger in the same transaction that flips the code
// to USED, so a crash can never leave "code consumed but nothing paid" or the
// reverse. The CAS on status='UNUSED' plus uk_rc_hash is the concurrency
// guard: two simultaneous redemptions of the same code produce one credit.
func (s *Service) Redeem(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req redeemRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	plain := strings.ToUpper(strings.TrimSpace(req.Code))
	if len(plain) < 6 || len(plain) > 64 {
		// An unusable code value is exactly that: a value problem the caller
		// fixes by typing another code.
		mw.WriteError(w, r, mw.InvalidField("code"))
		return
	}
	ctx := r.Context()
	now := s.now()

	resp, apiErr := s.redeemTx(ctx, r, uid, plain, now)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	s.invalidateBalance(ctx, uid)
	_ = mw.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) redeemTx(ctx context.Context, r *http.Request, uid int64, plain string, now time.Time) (redeemResponse, *apierr.APIError) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "fund_redeem_begin_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	sum := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(sum[:])

	var (
		codeID       int64
		status       string
		batchID      int64
		batchName    string
		faceValueFen int64
		creditTarget string
		validTo      time.Time
	)
	err = tx.QueryRowContext(ctx, `
		SELECT c.id, c.status, b.id, b.batch_name, b.face_value_fen,
		       b.credit_target, c.valid_to
		  FROM t_redeem_code c
		  JOIN t_redeem_batch b ON b.id = c.batch_id
		 WHERE c.code_hash = ?
		 FOR UPDATE`, hash).Scan(
		&codeID, &status, &batchID, &batchName, &faceValueFen, &creditTarget, &validTo)
	if errors.Is(err, sql.ErrNoRows) {
		// An unknown code is a wrong value, not a server fault. The caller
		// fixes it by entering another code, so it stays a body problem (the
		// T12 P0 rule: "would editing the named field make this succeed?").
		s.logWarn(ctx, "fund_redeem_unknown_code user="+itoa(uid))
		return redeemResponse{}, mw.InvalidField("code")
	}
	if err != nil {
		s.logError(ctx, "fund_redeem_lookup_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}
	switch {
	case status == redeemUsed:
		// A second attempt on a consumed code is worth a trail: it is either
		// a user re-click or code sharing.
		s.auditRedemption(ctx, uid, codeID, "REDEEM_REJECTED", map[string]any{
			"reason": "CODE_USED",
		})
		return redeemResponse{}, mw.InvalidField("code")
	case status == redeemVoid, status == redeemExpired, !validTo.After(now):
		// Expiry is state, but the caller's remedy is still "use another
		// code", which is a body edit; the audit row below records which
		// case it was for the operator. It is written outside this
		// transaction on purpose: the transaction rolls back, and an audit
		// row that vanishes with it would hide the attempt.
		s.auditRedemption(ctx, uid, codeID, "REDEEM_REJECTED", map[string]any{
			"reason": "CODE_EXPIRED", "status": status, "valid_to": validTo,
		})
		return redeemResponse{}, mw.InvalidField("code")
	}

	amountMicro := fenToMicro(faceValueFen)
	grant := ledger.GrantParams{
		UserID:         uid,
		BizType:        ledger.BizTypeRedeem,
		BonusBatchType: ledger.BatchTypeRedeem,
		IdemKeyBonus:   "redeem:" + itoa(codeID),
	}
	if strings.EqualFold(creditTarget, "RECHARGE") {
		grant.RechargeMicro = amountMicro
		grant.IdemKeyRecharge = "redeem:" + itoa(codeID)
		grant.IdemKeyBonus = ""
	} else {
		grant.BonusMicro = amountMicro
		days := s.bonusExpireDays(ctx, 0)
		expire := now.Add(time.Duration(days) * 24 * time.Hour)
		grant.BonusExpireAt = &expire
	}

	// CAS on the code: only an UNUSED row may be consumed. A concurrent
	// redemption loses here, before any credit is written.
	res, err := tx.ExecContext(ctx, `
		UPDATE t_redeem_code
		   SET status = 'USED', redeemed_by = ?, redeemed_at = ?
		 WHERE id = ? AND status = 'UNUSED'`, uid, now, codeID)
	if err != nil {
		s.logError(ctx, "fund_redeem_cas_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		s.logError(ctx, "fund_redeem_rows_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}
	if affected != 1 {
		return redeemResponse{}, mw.InvalidField("code")
	}

	result, gerr := s.Ledger.GrantTx(ctx, tx, grant, now)
	if gerr != nil {
		s.logError(ctx, "fund_redeem_grant_failed: "+gerr.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE t_redeem_batch SET redeemed_count = redeemed_count + 1 WHERE id = ?`,
		batchID); err != nil {
		s.logError(ctx, "fund_redeem_batch_count_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}
	ledgerID := result.BonusEntryID
	if result.BonusEntryID == 0 {
		ledgerID = result.RechargeEntryID
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE t_redeem_code SET ledger_id = ? WHERE id = ?`, ledgerID, codeID); err != nil {
		s.logError(ctx, "fund_redeem_ledger_link_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}

	if apiErr := s.auditRedemptionTx(ctx, tx, r, uid, codeID, "REDEEM", map[string]any{
		"batch_id": batchID, "batch_name": batchName,
		"face_value_fen": faceValueFen, "amount_micro": amountMicro,
		"credit_target": creditTarget, "ledger_id": ledgerID,
	}); apiErr != nil {
		return redeemResponse{}, apiErr
	}

	if err := tx.Commit(); err != nil {
		s.logError(ctx, "fund_redeem_commit_failed: "+err.Error())
		return redeemResponse{}, apierr.New(apierr.CodeInternalError)
	}

	expireAt := now
	if grant.BonusExpireAt != nil {
		expireAt = *grant.BonusExpireAt
	}
	return redeemResponse{
		BatchName:         batchName,
		AmountMicro:       amountMicro,
		Amount:            microToFen(amountMicro) / FenPerYuan,
		CreditTarget:      creditTarget,
		ExpireAt:          expireAt.UTC(),
		BalanceAfterMicro: result.BalanceAfterMicro,
	}, nil
}

// auditRedemption records a rejected attempt. It writes through the pool, not
// the caller's transaction: every caller reaches it on a path that rolls back.
func (s *Service) auditRedemption(ctx context.Context, uid, codeID int64, action string, payload map[string]any) {
	if err := s.Audit.Insert(ctx, s.DB, audit.Entry{
		OperatorID:   uid,
		OperatorRole: "PERSONAL",
		Module:       "FUND",
		Action:       action,
		TargetType:   "t_redeem_code",
		TargetID:     itoa(codeID),
		After:        mustJSON(payload),
		Result:       "REJECTED",
	}); err != nil {
		s.logWarn(ctx, "fund_redeem_audit_failed: "+err.Error())
	}
}

func (s *Service) auditRedemptionTx(ctx context.Context, tx *sql.Tx, r *http.Request, uid, codeID int64, action string, payload map[string]any) *apierr.APIError {
	if err := s.Audit.Insert(ctx, tx, audit.Entry{
		OperatorID:   uid,
		OperatorRole: "PERSONAL",
		OperatorIP:   audit.IPBytes(r),
		Module:       "FUND",
		Action:       action,
		TargetType:   "t_redeem_code",
		TargetID:     itoa(codeID),
		After:        mustJSON(payload),
		Result:       "OK",
	}); err != nil {
		s.logError(ctx, "fund_redeem_audit_failed: "+err.Error())
		return apierr.New(apierr.CodeInternalError)
	}
	return nil
}
