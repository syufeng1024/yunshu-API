package fund

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/ledger"
)

// t_invoice_apply.state — the five DDL values (TDS §1.5).
const (
	InvoiceStateApplied  = "APPLIED"
	InvoiceStateApproved = "APPROVED"
	InvoiceStateIssued   = "ISSUED"
	InvoiceStateRejected = "REJECTED"
	InvoiceStateVoided   = "VOIDED"
)

var invoiceStates = []string{
	InvoiceStateApplied,
	InvoiceStateApproved,
	InvoiceStateIssued,
	InvoiceStateRejected,
	InvoiceStateVoided,
}

const (
	invoiceTypeVATNormalE = "VAT_NORMAL_E"
	titleTypePersonal     = "PERSONAL"
	titleTypeCompany      = "COMPANY"
)

// invoiceBlockingStates are the states of an existing application that make a
// second application for the same order invalid. REJECTED/VOIDED do not.
var invoiceBlockingStates = []string{
	InvoiceStateApplied, InvoiceStateApproved, InvoiceStateIssued,
}

type createInvoiceRequest struct {
	RechargeOrderID int64  `json:"recharge_order_id"`
	TitleType       string `json:"title_type"`
	Title           string `json:"title"`
	TaxNo           string `json:"tax_no"`
	ReceiveEmail    string `json:"receive_email"`
	// AmountFen is optional; omitted means "the whole remaining amount".
	AmountFen      *int64 `json:"amount_fen"`
	IdempotencyKey string `json:"idempotency_key"`
}

type invoiceView struct {
	ID              int64      `json:"id"`
	ApplyNo         string     `json:"apply_no"`
	RechargeOrderID int64      `json:"recharge_order_id"`
	InvoiceType     string     `json:"invoice_type"`
	TitleType       string     `json:"title_type"`
	Title           string     `json:"title"`
	TaxNo           *string    `json:"tax_no"`
	ReceiveEmail    string     `json:"receive_email"`
	AmountFen       int64      `json:"amount_fen"`
	AmountYuan      int64      `json:"amount_yuan"`
	TaxRateBP       int64      `json:"tax_rate_bp"`
	State           string     `json:"state"`
	RejectReason    *string    `json:"reject_reason"`
	InvoiceURL      *string    `json:"invoice_url"`
	AuditorID       *int64     `json:"auditor_id"`
	IssuedAt        *time.Time `json:"issued_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type createInvoiceResponse struct {
	Invoice  invoiceView `json:"invoice"`
	Replayed bool        `json:"idempotent_replay"`
}

type invoiceListResponse struct {
	Items    []invoiceView `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}

const invoiceColumns = `
	id, apply_no, recharge_order_id, invoice_type, title_type, title, tax_no,
	receive_email, amount_fen, state, reject_reason, invoice_url, auditor_id,
	issued_at, created_at`

// taxRateBP returns the frozen V1.0 tax multiplier as basis points.
//
// TODO(Finance): pricing.tax_rate_multiplier is fixed at 1.00 (Q-03). Do not
// design tax-inclusive/exclusive splitting before Finance signs off; this
// function exists only so the number has one named home.
func (s *Service) taxRateBP(ctx context.Context) int64 {
	raw := s.configString(ctx, config.KeyPricingTaxRateMultiplier, "1.00")
	switch raw {
	case "1.00", "1.0", "1":
		return 10000
	default:
		// An operator may not change the rate for V1.0; keep 1.00 and make the
		// ignored value visible instead of silently applying it.
		s.logWarn(ctx, "fund_invoice_tax_rate_ignored: "+raw)
		return 10000
	}
}

// CreateInvoice implements POST /api/console/invoices (G-07 / F10-5).
func (s *Service) CreateInvoice(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req createInvoiceRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()

	// 1. Real-name gate (G-07 And). The API stub owns the verified_at column;
	// V1.0 keeps the branch even though verification ships later.
	var verifiedAt sql.NullTime
	err := s.DB.QueryRowContext(ctx,
		`SELECT verified_at FROM t_user WHERE id = ? AND deleted_at IS NULL`, uid).
		Scan(&verifiedAt)
	if err != nil {
		s.logError(ctx, "fund_invoice_user_read_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if !verifiedAt.Valid {
		mw.WriteError(w, r, apierr.New(apierr.CodeAccountNotVerified))
		return
	}

	if req.RechargeOrderID <= 0 {
		mw.WriteError(w, r, mw.InvalidField("recharge_order_id"))
		return
	}
	titleType := strings.ToUpper(strings.TrimSpace(req.TitleType))
	if titleType != titleTypePersonal && titleType != titleTypeCompany {
		mw.WriteError(w, r, mw.InvalidField("title_type"))
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len([]rune(title)) > 128 {
		mw.WriteError(w, r, mw.InvalidField("title"))
		return
	}
	taxNo := strings.TrimSpace(req.TaxNo)
	if titleType == titleTypeCompany && taxNo == "" {
		mw.WriteError(w, r, mw.InvalidField("tax_no"))
		return
	}
	if len(taxNo) > 32 {
		mw.WriteError(w, r, mw.InvalidField("tax_no"))
		return
	}
	email := strings.TrimSpace(req.ReceiveEmail)
	if email == "" || len(email) > 254 {
		mw.WriteError(w, r, mw.InvalidField("receive_email"))
		return
	}
	if _, perr := mail.ParseAddress(email); perr != nil {
		mw.WriteError(w, r, mw.InvalidField("receive_email"))
		return
	}
	if req.AmountFen != nil && *req.AmountFen < 0 {
		mw.WriteError(w, r, mw.InvalidField("amount_fen"))
		return
	}
	idemKey, apiErr := sanitizeIdemKey(r.Header.Get("Idempotency-Key"), req.IdempotencyKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	id, replayed, finalize, release, apiErr := s.reserveIdem(ctx, uid, idemScopeInvoice, idemKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if replayed {
		view, apiErr := s.invoiceByID(ctx, uid, id)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		_ = mw.WriteJSON(w, http.StatusOK, createInvoiceResponse{Invoice: view, Replayed: true})
		return
	}

	view, apiErr := s.insertInvoice(ctx, r, uid, invoiceInput{
		RechargeOrderID: req.RechargeOrderID,
		TitleType:       titleType,
		Title:           title,
		TaxNo:           taxNo,
		ReceiveEmail:    email,
		AmountFen:       req.AmountFen,
	})
	if apiErr != nil {
		release()
		mw.WriteError(w, r, apiErr)
		return
	}
	finalize(view.ID)
	_ = mw.WriteJSON(w, http.StatusCreated, createInvoiceResponse{Invoice: view})
}

type invoiceInput struct {
	RechargeOrderID int64
	TitleType       string
	Title           string
	TaxNo           string
	ReceiveEmail    string
	AmountFen       *int64
}

func (s *Service) insertInvoice(ctx context.Context, r *http.Request, uid int64, in invoiceInput) (invoiceView, *apierr.APIError) {
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "fund_invoice_begin_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Lock the order: concurrent applications for one order must serialise
	// on its pay_amount_fen / invoiced_fen pair.
	var (
		state       string
		payAmount   int64
		invoicedFen int64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT state, pay_amount_fen, invoiced_fen
		  FROM t_recharge_order
		 WHERE id = ? AND user_id = ? FOR UPDATE`,
		in.RechargeOrderID, uid).Scan(&state, &payAmount, &invoicedFen)
	if errors.Is(err, sql.ErrNoRows) {
		return invoiceView{}, mw.InvalidField("recharge_order_id")
	}
	if err != nil {
		s.logError(ctx, "fund_invoice_order_read_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}
	if state != OrderStatePaid {
		return invoiceView{}, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("订单状态为 %s，只有已支付订单可以开票", state))
	}

	var existingState string
	err = tx.QueryRowContext(ctx, `
		SELECT state FROM t_invoice_apply
		 WHERE recharge_order_id = ? AND user_id = ?
		   AND state IN ('APPLIED','APPROVED','ISSUED')
		 ORDER BY id DESC LIMIT 1`, in.RechargeOrderID, uid).Scan(&existingState)
	if err == nil {
		if existingState == InvoiceStateIssued {
			return invoiceView{}, apierr.Newf(apierr.CodeInvalidRequestBody,
				"该订单已开具发票，不可重复申请")
		}
		return invoiceView{}, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("该订单已有处理中的开票申请（%s）", existingState))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "fund_invoice_existing_check_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}

	remaining := payAmount - invoicedFen
	if remaining <= 0 {
		return invoiceView{}, apierr.Newf(apierr.CodeInvalidRequestBody,
			"该订单可开票金额为 0 分")
	}
	amount := remaining
	if in.AmountFen != nil && *in.AmountFen > 0 {
		amount = *in.AmountFen
	}
	if amount > remaining {
		return invoiceView{}, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("开票金额超过可开额度，剩余可开 %d 分（%d.%02d 元）",
				remaining, remaining/FenPerYuan, remaining%FenPerYuan))
	}

	applyNO := orderNo(now, randomDigits)
	var taxArg any
	if in.TaxNo != "" {
		taxArg = in.TaxNo
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_invoice_apply
			(apply_no, user_id, recharge_order_id, invoice_type, title_type,
			 title, tax_no, receive_email, amount_fen, state, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?, 'APPLIED', ?,?)`,
		applyNO, uid, in.RechargeOrderID, invoiceTypeVATNormalE, in.TitleType,
		in.Title, taxArg, in.ReceiveEmail, amount, now, now)
	if err != nil {
		s.logError(ctx, "fund_invoice_insert_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}
	invoiceID, err := res.LastInsertId()
	if err != nil {
		s.logError(ctx, "fund_invoice_id_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}

	if err := s.Audit.Insert(ctx, tx, audit.Entry{
		OperatorID:   uid,
		OperatorRole: "PERSONAL",
		OperatorIP:   audit.IPBytes(r),
		Module:       "INVOICE",
		Action:       "INVOICE_APPLY",
		TargetType:   "t_invoice_apply",
		TargetID:     itoa(invoiceID),
		Result:       "OK",
	}); err != nil {
		s.logWarn(ctx, "fund_invoice_audit_failed: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		s.logError(ctx, "fund_invoice_commit_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}
	committed = true
	return s.invoiceByID(ctx, uid, invoiceID)
}

func (s *Service) invoiceByID(ctx context.Context, uid, id int64) (invoiceView, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+invoiceColumns+`
		  FROM t_invoice_apply WHERE id = ? AND user_id = ?`, id, uid)
	v, err := scanInvoice(row)
	if errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "fund_invoice_missing: id="+itoa(id))
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}
	if err != nil {
		s.logError(ctx, "fund_invoice_read_failed: "+err.Error())
		return invoiceView{}, apierr.New(apierr.CodeInternalError)
	}
	v.TaxRateBP = s.taxRateBP(ctx)
	return v, nil
}

func scanInvoice(sc scanner) (invoiceView, error) {
	var (
		v            invoiceView
		taxNo        sql.NullString
		rejectReason sql.NullString
		invoiceURL   sql.NullString
		auditorID    sql.NullInt64
		issuedAt     sql.NullTime
	)
	if err := sc.Scan(&v.ID, &v.ApplyNo, &v.RechargeOrderID, &v.InvoiceType,
		&v.TitleType, &v.Title, &taxNo, &v.ReceiveEmail, &v.AmountFen,
		&v.State, &rejectReason, &invoiceURL, &auditorID, &issuedAt,
		&v.CreatedAt); err != nil {
		return invoiceView{}, err
	}
	if taxNo.Valid {
		v.TaxNo = &taxNo.String
	}
	if rejectReason.Valid {
		v.RejectReason = &rejectReason.String
	}
	if invoiceURL.Valid {
		v.InvoiceURL = &invoiceURL.String
	}
	if auditorID.Valid {
		v.AuditorID = &auditorID.Int64
	}
	if issuedAt.Valid {
		t := issuedAt.Time.UTC()
		v.IssuedAt = &t
	}
	v.AmountYuan = v.AmountFen / FenPerYuan
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}

// ListInvoices implements GET /api/console/invoices.
func (s *Service) ListInvoices(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	page, apiErr := mw.ParsePage(r, "created_at", "issued_at")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	states, apiErr := parseInvoiceStatus(r.URL.Query().Get("status"))
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
		`SELECT COUNT(*) FROM t_invoice_apply WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "fund_invoice_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	orderBy := "created_at"
	if page.Sort == "issued_at" {
		orderBy = "issued_at"
	}
	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `SELECT `+invoiceColumns+`
		  FROM t_invoice_apply WHERE `+whereSQL+`
		  ORDER BY `+orderBy+` `+direction+`, id `+direction+`
		  LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "fund_invoice_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	taxBP := s.taxRateBP(ctx)
	items := make([]invoiceView, 0, page.Size)
	for rows.Next() {
		v, err := scanInvoice(rows)
		if err != nil {
			s.logError(ctx, "fund_invoice_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		v.TaxRateBP = taxBP
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "fund_invoice_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, invoiceListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

func parseInvoiceStatus(raw string) ([]string, *apierr.APIError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return nil, nil
	case "applied":
		return []string{InvoiceStateApplied}, nil
	case "approved":
		return []string{InvoiceStateApproved}, nil
	case "issued":
		return []string{InvoiceStateIssued}, nil
	case "rejected":
		return []string{InvoiceStateRejected}, nil
	case "voided":
		return []string{InvoiceStateVoided}, nil
	default:
		return nil, mw.InvalidQueryField("status")
	}
}

// IssueInvoice moves APPLIED/APPROVED -> ISSUED, reserves the amount on the
// recharge order and notifies the user (G-07 / F20-4). T16 calls it after the
// finance operator uploads the PDF.
func (s *Service) IssueInvoice(ctx context.Context, invoiceID, auditorID int64, invoiceURL string) error {
	invoiceURL = strings.TrimSpace(invoiceURL)
	if invoiceURL == "" || len(invoiceURL) > 512 {
		return fmt.Errorf("%w: invoice_url is required (max 512)", ledger.ErrInvalidParam)
	}
	var (
		userID        int64
		orderID       int64
		amount        int64
		state         string
		alreadyIssued bool
	)
	err := s.withInvoiceTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT user_id, recharge_order_id, amount_fen, state
			  FROM t_invoice_apply WHERE id = ? FOR UPDATE`, invoiceID).
			Scan(&userID, &orderID, &amount, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvoiceState
		}
		if err != nil {
			return err
		}
		if state == InvoiceStateIssued {
			userID, alreadyIssued = 0, true
			return nil // idempotent retry, no second notification
		}
		if state != InvoiceStateApplied && state != InvoiceStateApproved {
			return fmt.Errorf("%w: %s -> ISSUED", ErrInvoiceState, state)
		}

		var payAmount, invoicedFen int64
		if err := tx.QueryRowContext(ctx, `
			SELECT pay_amount_fen, invoiced_fen FROM t_recharge_order
			 WHERE id = ? FOR UPDATE`, orderID).Scan(&payAmount, &invoicedFen); err != nil {
			return err
		}
		if invoicedFen+amount > payAmount {
			return fmt.Errorf("%w: invoiced=%d amount=%d paid=%d",
				ledger.ErrInvalidParam, invoicedFen, amount, payAmount)
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_invoice_apply
			   SET state = 'ISSUED', invoice_url = ?, issued_at = ?,
			       auditor_id = ?, updated_at = ?
			 WHERE id = ? AND state IN ('APPLIED','APPROVED')`,
			invoiceURL, now, auditorID, now, invoiceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_recharge_order SET invoiced_fen = invoiced_fen + ?, updated_at = ?
			 WHERE id = ?`, amount, now, orderID); err != nil {
			return err
		}
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: auditorID, OperatorRole: "FINANCE", Module: "INVOICE",
			Action: "INVOICE_ISSUE", TargetType: "t_invoice_apply",
			TargetID: itoa(invoiceID), Result: "OK",
		})
	})
	if err != nil {
		return err
	}
	if alreadyIssued {
		return nil
	}
	s.notify(ctx, userID, eventInvoiceProgress, "发票已开具",
		fmt.Sprintf("您的发票（申请单 %d）已开具，下载地址：%s", invoiceID, invoiceURL))
	return nil
}

// ErrInvoiceState means the invoice cannot move to the requested state.
var ErrInvoiceState = errors.New("fund: invoice state does not allow this transition")

func (s *Service) withInvoiceTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fund: begin invoice transaction: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
