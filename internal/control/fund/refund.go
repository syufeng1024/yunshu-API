package fund

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ledger"
)

// t_refund_order.state — the six values in the DDL (TDS §1.5). The task card's
// PENDING / REFUNDING / REFUNDED / REFUND_FAILED do not exist in the table;
// errata E-26 fixes the document, not the schema.
const (
	RefundStateApplied  = "APPLIED"
	RefundStateApproved = "APPROVED"
	RefundStateRejected = "REJECTED"
	RefundStatePaying   = "PAYING"
	RefundStatePaid     = "PAID"
	RefundStateFailed   = "FAILED"
)

// refundStates is the complete enumeration; the docs consistency test compares
// its set with docs/fund-flow.md.
var refundStates = []string{
	RefundStateApplied,
	RefundStateApproved,
	RefundStateRejected,
	RefundStatePaying,
	RefundStatePaid,
	RefundStateFailed,
}

// ErrSelfAudit refuses "the applicant is also the auditor" (PRD §6.6 F20-3,
// A13). The same rule covers refunds and quota applies.
var ErrSelfAudit = errors.New("fund: applicant cannot audit their own request")

// ErrRefundState means the requested transition is not reachable from the
// row's current state.
var ErrRefundState = errors.New("fund: refund state does not allow this transition")

// refundActiveStates are the states that still hold frozen money. At most one
// such row may exist per account: frozen_refund_micro is account-level, and
// two concurrent refunds would each quote against a base that includes the
// other's freeze.
var refundActiveStates = []string{RefundStateApplied, RefundStateApproved, RefundStatePaying}

type createRefundRequest struct {
	RechargeOrderID int64  `json:"recharge_order_id"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type refundView struct {
	ID              int64      `json:"id"`
	RefundNo        string     `json:"refund_no"`
	RechargeOrderID int64      `json:"recharge_order_id"`
	ApplyMicro      int64      `json:"apply_micro"`
	ApplyCredits    int64      `json:"apply_credits"`
	RefundMicro     int64      `json:"refund_micro"`
	RefundCredits   int64      `json:"refund_credits"`
	RefundFen       int64      `json:"refund_fen"`
	RefundYuan      int64      `json:"refund_yuan"`
	FeeFen          int64      `json:"fee_fen"`
	FeeYuan         int64      `json:"fee_yuan"`
	State           string     `json:"state"`
	RejectReason    *string    `json:"reject_reason"`
	FailReason      *string    `json:"fail_reason"`
	PayTxnID        *string    `json:"pay_txn_id"`
	AuditorID       *int64     `json:"auditor_id"`
	AuditedAt       *time.Time `json:"audited_at"`
	PaidAt          *time.Time `json:"paid_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type createRefundResponse struct {
	Refund   refundView `json:"refund"`
	Replayed bool       `json:"idempotent_replay"`
}

type refundListResponse struct {
	Items    []refundView `json:"items"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int64        `json:"total"`
}

const refundColumns = `
	id, refund_no, recharge_order_id, apply_micro, refund_micro, refund_fen,
	fee_fen, state, reject_reason, fail_reason, pay_txn_id, auditor_id,
	audited_at, paid_at, created_at`

// CreateRefund implements POST /api/console/refunds (G-04 / F10-5).
//
// The quote and the freeze happen in one transaction that already holds the
// account row lock, so a concurrent settlement cannot consume the refundable
// base between "how much can be refunded" and "freeze that much".
func (s *Service) CreateRefund(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req createRefundRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.RechargeOrderID <= 0 {
		mw.WriteError(w, r, mw.InvalidField("recharge_order_id"))
		return
	}
	idemKey, apiErr := sanitizeIdemKey(r.Header.Get("Idempotency-Key"), req.IdempotencyKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()

	id, replayed, finalize, release, apiErr := s.reserveIdem(ctx, uid, idemScopeRefund, idemKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if replayed {
		view, apiErr := s.refundByID(ctx, uid, id)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		_ = mw.WriteJSON(w, http.StatusOK, createRefundResponse{Refund: view, Replayed: true})
		return
	}

	view, apiErr := s.insertRefund(ctx, r, uid, req.RechargeOrderID)
	if apiErr != nil {
		release()
		mw.WriteError(w, r, apiErr)
		return
	}
	finalize(view.ID)
	_ = mw.WriteJSON(w, http.StatusCreated, createRefundResponse{Refund: view})
}

func (s *Service) insertRefund(ctx context.Context, r *http.Request, uid, rechargeOrderID int64) (refundView, *apierr.APIError) {
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "fund_refund_begin_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Lock order: account, then the recharge order. Both the quote and the
	// freeze below depend on the account row being stable.
	if _, err := ledger.LockAccount(ctx, tx, uid); err != nil {
		s.logError(ctx, "fund_refund_lock_account_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	// A plain read, not FOR UPDATE: another operator's MarkRefundFailed may
	// hold the refund row and be waiting for the account lock this transaction
	// already holds. Taking a lock here would close that cycle. The account
	// lock still serialises every CreateRefund for this user, and the snapshot
	// is taken after the lock is acquired.
	var activeID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM t_refund_order
		 WHERE user_id = ? AND state IN ('APPLIED','APPROVED','PAYING')
		 LIMIT 1`, uid).Scan(&activeID)
	if err == nil {
		return refundView{}, apierr.Newf(apierr.CodeResourceStateConflict,
			"该账号已有进行中的退款单，请等待处理完成后再申请")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "fund_refund_active_check_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}

	var orderState string
	err = tx.QueryRowContext(ctx, `
		SELECT state FROM t_recharge_order
		 WHERE id = ? AND user_id = ? FOR UPDATE`, rechargeOrderID, uid).Scan(&orderState)
	if errors.Is(err, sql.ErrNoRows) {
		return refundView{}, mw.InvalidField("recharge_order_id")
	}
	if err != nil {
		s.logError(ctx, "fund_refund_order_read_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	if orderState != OrderStatePaid {
		return refundView{}, apierr.Newf(apierr.CodeResourceStateConflict,
			fmt.Sprintf("订单当前状态为 %s，只有已支付订单可以退款", orderState))
	}

	quote, err := s.Ledger.QuoteRefundTx(ctx, tx, ledger.RefundQuoteParams{
		UserID:          uid,
		RechargeOrderID: &rechargeOrderID,
	})
	if err != nil {
		s.logError(ctx, "fund_refund_quote_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	if !quote.Eligible {
		return refundView{}, apierr.Newf(apierr.CodeResourceStateConflict, quote.Reason)
	}

	refundNO := orderNo(now, randomDigits)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_refund_order
			(refund_no, user_id, recharge_order_id, apply_micro, refund_micro,
			 refund_fen, fee_fen, state, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?, 'APPLIED', ?,?)`,
		refundNO, uid, rechargeOrderID, quote.RefundableMicro, quote.RefundableMicro,
		quote.RefundFen, quote.FeeFen, now, now)
	if err != nil {
		s.logError(ctx, "fund_refund_insert_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	refundID, err := res.LastInsertId()
	if err != nil {
		s.logError(ctx, "fund_refund_id_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	if err := insertReviewTaskTx(ctx, tx, reviewBizRefundApply, refundID, uid, now); err != nil {
		s.logError(ctx, "fund_refund_review_enqueue_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}

	if err := s.Ledger.FreezeRefundTx(ctx, tx, uid, quote.RefundableMicro); err != nil {
		s.logError(ctx, "fund_refund_freeze_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}

	// refundable_micro is the materialized "what is still refundable today".
	// Consumption does not maintain it (T04 kernel), so the refund transaction
	// refreshes it from the same quote it just froze.
	upd, err := tx.ExecContext(ctx, `
		UPDATE t_recharge_order
		   SET state = 'REFUNDING', refundable_micro = ?, updated_at = ?
		 WHERE id = ? AND user_id = ? AND state = 'PAID'`,
		quote.RefundableMicro, now, rechargeOrderID, uid)
	if err != nil {
		s.logError(ctx, "fund_refund_order_state_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	if n, _ := upd.RowsAffected(); n != 1 {
		return refundView{}, apierr.Newf(apierr.CodeResourceStateConflict,
			"订单状态已变化，请刷新后重试")
	}

	after, merr := jsonx.Marshal(map[string]any{
		"refund_no":         refundNO,
		"recharge_order_id": rechargeOrderID,
		"apply_micro":       quote.RefundableMicro,
		"refund_fen":        quote.RefundFen,
		"fee_fen":           quote.FeeFen,
		"state":             RefundStateApplied,
	})
	if merr == nil {
		if aerr := s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID:   uid,
			OperatorRole: "PERSONAL",
			OperatorIP:   audit.IPBytes(r),
			Module:       "REFUND",
			Action:       "REFUND_APPLY",
			TargetType:   "t_refund_order",
			TargetID:     itoa(refundID),
			After:        after,
			Result:       "OK",
		}); aerr != nil {
			s.logWarn(ctx, "fund_refund_audit_failed: "+aerr.Error())
		}
	}

	if err := tx.Commit(); err != nil {
		s.logError(ctx, "fund_refund_commit_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	committed = true

	view, apiErr := s.refundByID(ctx, uid, refundID)
	if apiErr != nil {
		return refundView{}, apiErr
	}
	return view, nil
}

func (s *Service) refundByID(ctx context.Context, uid, id int64) (refundView, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+refundColumns+`
		  FROM t_refund_order WHERE id = ? AND user_id = ?`, id, uid)
	v, err := scanRefund(row)
	if errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "fund_refund_missing: id="+itoa(id))
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	if err != nil {
		s.logError(ctx, "fund_refund_read_failed: "+err.Error())
		return refundView{}, apierr.New(apierr.CodeInternalError)
	}
	return v, nil
}

func scanRefund(sc scanner) (refundView, error) {
	var (
		v            refundView
		rejectReason sql.NullString
		failReason   sql.NullString
		payTxn       sql.NullString
		auditorID    sql.NullInt64
		auditedAt    sql.NullTime
		paidAt       sql.NullTime
	)
	if err := sc.Scan(&v.ID, &v.RefundNo, &v.RechargeOrderID, &v.ApplyMicro,
		&v.RefundMicro, &v.RefundFen, &v.FeeFen, &v.State, &rejectReason,
		&failReason, &payTxn, &auditorID, &auditedAt, &paidAt, &v.CreatedAt); err != nil {
		return refundView{}, err
	}
	if rejectReason.Valid {
		v.RejectReason = &rejectReason.String
	}
	if failReason.Valid {
		v.FailReason = &failReason.String
	}
	if payTxn.Valid {
		v.PayTxnID = &payTxn.String
	}
	if auditorID.Valid {
		v.AuditorID = &auditorID.Int64
	}
	if auditedAt.Valid {
		t := auditedAt.Time.UTC()
		v.AuditedAt = &t
	}
	if paidAt.Valid {
		t := paidAt.Time.UTC()
		v.PaidAt = &t
	}
	v.ApplyCredits = v.ApplyMicro / MicroPerCredit
	v.RefundCredits = v.RefundMicro / MicroPerCredit
	v.RefundYuan = v.RefundFen / FenPerYuan
	v.FeeYuan = v.FeeFen / FenPerYuan
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}

// ListRefunds implements GET /api/console/refunds.
func (s *Service) ListRefunds(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	page, apiErr := mw.ParsePage(r, "created_at", "paid_at")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	states, apiErr := parseRefundStatus(r.URL.Query().Get("status"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	where := []string{"user_id = ?"}
	args := []any{uid}
	if len(states) == 1 {
		where = append(where, "state = ?")
		args = append(args, states[0])
	} else if len(states) > 1 {
		ph := make([]string, len(states))
		for i, st := range states {
			ph[i] = "?"
			args = append(args, st)
		}
		where = append(where, "state IN ("+strings.Join(ph, ",")+")")
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_refund_order WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "fund_refund_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	orderBy := "created_at"
	if page.Sort == "paid_at" {
		orderBy = "paid_at"
	}
	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `SELECT `+refundColumns+`
		  FROM t_refund_order WHERE `+whereSQL+`
		  ORDER BY `+orderBy+` `+direction+`, id `+direction+`
		  LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "fund_refund_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]refundView, 0, page.Size)
	for rows.Next() {
		v, err := scanRefund(rows)
		if err != nil {
			s.logError(ctx, "fund_refund_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "fund_refund_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, refundListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

func parseRefundStatus(raw string) ([]string, *apierr.APIError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return nil, nil
	case "applied":
		return []string{RefundStateApplied}, nil
	case "approved":
		return []string{RefundStateApproved}, nil
	case "rejected":
		return []string{RefundStateRejected}, nil
	case "paying":
		return []string{RefundStatePaying}, nil
	case "paid":
		return []string{RefundStatePaid}, nil
	case "failed":
		return []string{RefundStateFailed}, nil
	default:
		return nil, mw.InvalidQueryField("status")
	}
}

// ---------------------------------------------------------------------------
// Exported state machine (T16 审核中心 calls these; the console has no
// approve/pay endpoints of its own).
// ---------------------------------------------------------------------------

func (s *Service) withRefundTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fund: begin refund transaction: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fund: commit refund transaction: %w", err)
	}
	return nil
}

func (s *Service) lockRefundTx(ctx context.Context, tx *sql.Tx, id int64) (userID, orderID int64, applyMicro int64, state string, err error) {
	err = tx.QueryRowContext(ctx, `
		SELECT user_id, recharge_order_id, apply_micro, state
		  FROM t_refund_order WHERE id = ? FOR UPDATE`, id).
		Scan(&userID, &orderID, &applyMicro, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, "", ErrRefundState
	}
	return
}

// ApproveRefund moves APPLIED -> APPROVED. An applicant cannot approve their
// own refund; the attempt is recorded with result REJECTED.
func (s *Service) ApproveRefund(ctx context.Context, refundID, auditorID int64) error {
	denied := false
	err := s.withRefundTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		userID, _, _, state, err := s.lockRefundTx(ctx, tx, refundID)
		if err != nil {
			return err
		}
		if state != RefundStateApplied {
			return fmt.Errorf("%w: %s -> APPROVED", ErrRefundState, state)
		}
		now := s.now()
		if userID == auditorID {
			denied = true
			return s.Audit.Insert(ctx, tx, audit.Entry{
				OperatorID:   auditorID,
				OperatorRole: "OPS",
				Module:       "REFUND",
				Action:       "APPROVE",
				TargetType:   "t_refund_order",
				TargetID:     itoa(refundID),
				Reason:       "SELF_AUDIT: 申请人不得自审",
				Result:       "REJECTED",
			})
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_refund_order
			   SET state = 'APPROVED', auditor_id = ?, audited_at = ?, updated_at = ?
			 WHERE id = ? AND state = 'APPLIED'`,
			auditorID, now, now, refundID); err != nil {
			return err
		}
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID:   auditorID,
			OperatorRole: "OPS",
			Module:       "REFUND",
			Action:       "APPROVE",
			TargetType:   "t_refund_order",
			TargetID:     itoa(refundID),
			Result:       "OK",
		})
	})
	if err != nil {
		return err
	}
	if denied {
		return ErrSelfAudit
	}
	return nil
}

// StartRefundPayout moves APPROVED -> PAYING.
func (s *Service) StartRefundPayout(ctx context.Context, refundID int64) error {
	return s.withRefundTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, _, _, state, err := s.lockRefundTx(ctx, tx, refundID)
		if err != nil {
			return err
		}
		if state != RefundStateApproved {
			return fmt.Errorf("%w: %s -> PAYING", ErrRefundState, state)
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_refund_order SET state = 'PAYING', updated_at = ?
			 WHERE id = ? AND state = 'APPROVED'`, now, refundID); err != nil {
			return err
		}
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: 0, OperatorRole: "SYSTEM", Module: "REFUND",
			Action: "PAY", TargetType: "t_refund_order", TargetID: itoa(refundID),
			Result: "OK",
		})
	})
}

// MarkRefundPaid moves PAYING -> PAID by delegating the money movement to
// ledger.Refund, which deducts the unspent recharge batches and releases this
// order's frozen amount in one transaction. The recharge order becomes
// REFUNDED when no refundable credit is left, PARTIAL_REFUNDED otherwise.
func (s *Service) MarkRefundPaid(ctx context.Context, refundID int64, payTxnID string) (ledger.RefundResult, error) {
	payTxnID = strings.TrimSpace(payTxnID)
	if payTxnID == "" || len(payTxnID) > 64 {
		return ledger.RefundResult{}, fmt.Errorf("%w: pay_txn_id is required (max 64)", ledger.ErrInvalidParam)
	}
	var (
		userID   int64
		orderID  int64
		stateVal string
	)
	if err := s.DB.QueryRowContext(ctx, `
		SELECT user_id, recharge_order_id, state FROM t_refund_order WHERE id = ?`,
		refundID).Scan(&userID, &orderID, &stateVal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ledger.RefundResult{}, ErrRefundState
		}
		return ledger.RefundResult{}, err
	}
	if stateVal != RefundStatePaying && stateVal != RefundStatePaid {
		return ledger.RefundResult{}, fmt.Errorf("%w: %s -> PAID", ErrRefundState, stateVal)
	}

	result, err := s.Ledger.Refund(ctx, ledger.RefundParams{
		UserID:          userID,
		RechargeOrderID: orderID,
		RefundOrderID:   refundID,
	})
	if err != nil {
		return ledger.RefundResult{}, err
	}

	now := s.now()
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE t_refund_order SET pay_txn_id = ?, updated_at = ?
		 WHERE id = ? AND pay_txn_id IS NULL`, payTxnID, now, refundID); err != nil {
		s.logWarn(ctx, "fund_refund_pay_txn_update_failed: "+err.Error())
	}
	var remaining int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT refundable_micro FROM t_recharge_order WHERE id = ?`, orderID).
		Scan(&remaining); err != nil {
		s.logWarn(ctx, "fund_refund_order_remaining_failed: "+err.Error())
	}
	nextState := OrderStatePartialRefunded
	if remaining <= 0 {
		nextState = OrderStateRefunded
	}
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE t_recharge_order SET state = ?, updated_at = ?
		 WHERE id = ? AND state = 'REFUNDING'`, nextState, now, orderID); err != nil {
		s.logWarn(ctx, "fund_refund_order_state_failed: "+err.Error())
	}

	if err := s.Audit.Insert(ctx, s.DB, audit.Entry{
		OperatorID: 0, OperatorRole: "SYSTEM", Module: "REFUND",
		Action: "REFUND_PAID", TargetType: "t_refund_order", TargetID: itoa(refundID),
		Result: "OK",
	}); err != nil {
		s.logWarn(ctx, "fund_refund_audit_failed: "+err.Error())
	}

	s.notify(ctx, userID, eventRefundArrived, "退款已到账",
		fmt.Sprintf("退款单 %d 已原路退回 %d 分（%d.%02d 元）。",
			refundID, result.RefundFen, result.RefundFen/FenPerYuan, result.RefundFen%FenPerYuan))
	return result, nil
}

// MarkRefundFailed moves PAYING -> FAILED, releases the freeze and returns the
// recharge order to PAID. The balance totals do not move (A10).
func (s *Service) MarkRefundFailed(ctx context.Context, refundID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
		return fmt.Errorf("%w: fail_reason is required (max 255)", ledger.ErrInvalidParam)
	}
	var notifyUser int64
	var applyMicro int64
	err := s.withRefundTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		userID, orderID, apply, state, err := s.lockRefundTx(ctx, tx, refundID)
		if err != nil {
			return err
		}
		if state == RefundStateFailed {
			return nil // idempotent retry
		}
		if state != RefundStatePaying {
			return fmt.Errorf("%w: %s -> FAILED", ErrRefundState, state)
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_refund_order
			   SET state = 'FAILED', fail_reason = ?, updated_at = ?
			 WHERE id = ? AND state = 'PAYING'`, reason, now, refundID); err != nil {
			return err
		}
		if err := s.Ledger.ReleaseRefundTx(ctx, tx, userID, apply); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_recharge_order SET state = 'PAID', updated_at = ?
			 WHERE id = ? AND state = 'REFUNDING'`, now, orderID); err != nil {
			return err
		}
		notifyUser, applyMicro = userID, apply
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: 0, OperatorRole: "SYSTEM", Module: "REFUND",
			Action: "REFUND_FAILED", TargetType: "t_refund_order",
			TargetID: itoa(refundID), Reason: reason, Result: "OK",
		})
	})
	if err != nil {
		return err
	}
	if notifyUser == 0 {
		return nil // already FAILED on a previous call
	}
	s.notify(ctx, notifyUser, eventRefundArrived, "退款失败",
		fmt.Sprintf("退款单 %d 的 %d 分未能原路退回：%s。冻结已解除，积分仍可正常使用；如需继续退款请联系客服。",
			refundID, applyMicro/MicroPerCredit, reason))
	return nil
}

// RejectRefund moves APPLIED -> REJECTED. The applicant cannot be the auditor,
// and the frozen amount is released without changing the balance.
func (s *Service) RejectRefund(ctx context.Context, refundID, auditorID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
		return fmt.Errorf("%w: reject_reason is required (max 255)", ledger.ErrInvalidParam)
	}
	denied := false
	err := s.withRefundTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		userID, orderID, apply, state, err := s.lockRefundTx(ctx, tx, refundID)
		if err != nil {
			return err
		}
		if state != RefundStateApplied {
			return fmt.Errorf("%w: %s -> REJECTED", ErrRefundState, state)
		}
		now := s.now()
		if userID == auditorID {
			denied = true
			return s.Audit.Insert(ctx, tx, audit.Entry{
				OperatorID: auditorID, OperatorRole: "OPS", Module: "REFUND",
				Action: "REJECT", TargetType: "t_refund_order", TargetID: itoa(refundID),
				Reason: "SELF_AUDIT: 申请人不得自审", Result: "REJECTED",
			})
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_refund_order
			   SET state = 'REJECTED', reject_reason = ?, auditor_id = ?,
			       audited_at = ?, updated_at = ?
			 WHERE id = ? AND state = 'APPLIED'`,
			reason, auditorID, now, now, refundID); err != nil {
			return err
		}
		if err := s.Ledger.ReleaseRefundTx(ctx, tx, userID, apply); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_recharge_order SET state = 'PAID', updated_at = ?
			 WHERE id = ? AND state = 'REFUNDING'`, now, orderID); err != nil {
			return err
		}
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: auditorID, OperatorRole: "OPS", Module: "REFUND",
			Action: "REJECT", TargetType: "t_refund_order", TargetID: itoa(refundID),
			Reason: reason, Result: "OK",
		})
	})
	if err != nil {
		return err
	}
	if denied {
		return ErrSelfAudit
	}
	return nil
}
