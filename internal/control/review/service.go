// Package review implements the T16 review centre: one queue for the five
// document types an operator has to act on.
//
// The queue is not a second state machine. t_review_task records who is
// waiting and what the operator decided; the business tables (t_quota_apply,
// t_refund_order, t_invoice_apply, ...) keep owning their own state. Every
// side effect therefore goes through the exporting service — this package
// never writes a credit balance and never invents a second approval path.
//
// Three rules are enforced here, not in the browser:
//
//   - The default tab is PENDING and the order is created_at ASC (first come,
//     first served). Amount is deliberately not a sort key.
//   - A rejection must carry a reason. It is stored in decision_note and is
//     what the applicant sees at the corresponding console page.
//   - The applicant may never decide their own document. Both approve and
//     reject answer 403 permission_denied and write an audit row naming the
//     refused attempt.
package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/fund"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// t_review_task.biz_type — exactly the five DDL values (0015). Adding a sixth
// value would require a migration and a business owner; neither exists.
const (
	BizQuotaApply    = "QUOTA_APPLY"
	BizInvoiceApply  = "INVOICE_APPLY"
	BizRefundApply   = "REFUND_APPLY"
	BizRedeemBatch   = "REDEEM_BATCH"
	BizBalanceAdjust = "BALANCE_ADJUST"
)

// t_review_task.state — the three values the 0015 DDL permits. The PRD also
// names CANCELLED and per-type ISSUED/REFUNDED, but those live on the business
// rows; the queue row itself has no such state (T03 left this to T16 and the
// delivered DDL settled on three).
const (
	StatePending  = "PENDING"
	StateApproved = "APPROVED"
	StateRejected = "REJECTED"
)

var bizTypeSet = map[string]bool{
	BizQuotaApply:    true,
	BizInvoiceApply:  true,
	BizRefundApply:   true,
	BizRedeemBatch:   true,
	BizBalanceAdjust: true,
}

const (
	// decisionNoteMax is t_review_task.decision_note varchar(255).
	decisionNoteMax = 255
	// summaryMax keeps a long apply_reason from turning one row into a wall.
	summaryMax = 200
)

// Service owns the review queue.
type Service struct {
	DB     *sql.DB
	Audit  *audit.Writer
	Now    clock.Clock
	Logger *obslog.Logger

	// Fund is the T13/T04 seam. Approval transitions are called on it; this
	// package has no balance write of its own.
	Fund *fund.Service

	// Ledger is the only way a BALANCE_ADJUST document can move money. The
	// ledger store is shared with Fund in production so entry_no comes from one
	// generator; the fallback keeps package tests and a hand-wired Service
	// honest.
	Ledger *ledger.Store
}

// New wires the service.
func New(db *sql.DB, auditWriter *audit.Writer, now clock.Clock, logger *obslog.Logger, funds *fund.Service) *Service {
	if now == nil {
		now = clock.Real{}
	}
	if auditWriter == nil {
		auditWriter = &audit.Writer{DB: db, Now: now}
	}
	var ldg *ledger.Store
	if funds != nil {
		ldg = funds.Ledger
	}
	if ldg == nil {
		ldg = ledger.NewStore(db, now)
	}
	return &Service{
		DB: db, Audit: auditWriter, Now: now, Logger: logger,
		Fund: funds, Ledger: ldg,
	}
}

func (s *Service) now() time.Time { return s.Now.Now().UTC() }

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "review", Status: http.StatusInternalServerError})
	}
}

func (s *Service) logWarn(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Warn(ctx, msg, obslog.Fields{Stage: "review"})
	}
}

// reviewTask is the queue row before enrichment.
type reviewTask struct {
	ID           int64
	BizType      string
	BizID        int64
	ApplicantID  int64
	State        string
	AssigneeID   *int64
	DecisionNote *string
	DecidedAt    *time.Time
	CreatedAt    time.Time
}

// reviewItem is the API shape: the queue row plus the business facts an
// operator needs to decide without opening another page.
type reviewItem struct {
	ID            int64  `json:"id"`
	BizType       string `json:"biz_type"`
	BizID         int64  `json:"biz_id"`
	BizNo         string `json:"biz_no,omitempty"`
	ApplicantID   int64  `json:"applicant_id"`
	ApplicantName string `json:"applicant_name"`
	AmountMicro   *int64 `json:"amount_micro,omitempty"`
	AmountFen     *int64 `json:"amount_fen,omitempty"`
	// Direction is set only for BALANCE_ADJUST (INCREASE / DECREASE). It is
	// separate from summary so the console can render the sign next to the
	// amount without parsing Chinese text.
	Direction     *string    `json:"direction,omitempty"`
	Summary       string     `json:"summary"`
	BusinessState string     `json:"business_state,omitempty"`
	State         string     `json:"state"`
	AssigneeID    *int64     `json:"assignee_id,omitempty"`
	DecisionNote  *string    `json:"decision_note,omitempty"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	// Anomaly marks a queue row whose business row no longer exists. The list
	// still answers 200: one orphaned row must not blank the whole queue.
	Anomaly       bool    `json:"anomaly"`
	AnomalyReason *string `json:"anomaly_reason,omitempty"`
}

type reviewListResponse struct {
	Items    []reviewItem `json:"items"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int64        `json:"total"`
}

type reviewDetailResponse struct {
	Review reviewItem `json:"review"`
}

// decisionRequest is the body of approve and reject. Reason is mandatory for
// reject and optional (stored as decision_note) for approve; invoice_url is
// required when approving an INVOICE_APPLY because IssueInvoice refuses to
// mark a document ISSUED without the file.
type decisionRequest struct {
	Reason     string `json:"reason"`
	InvoiceURL string `json:"invoice_url"`
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) *apierr.APIError {
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		return mw.InvalidField("body")
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return nil
	}
	if err := jsonx.Unmarshal(payload, dst); err != nil {
		return mw.InvalidField("body")
	}
	return nil
}

func actor(r *http.Request) (int64, string, *apierr.APIError) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok || sess == nil {
		return 0, "", mw.SessionMissing()
	}
	return sess.UID, sess.Role, nil
}

func pathID(r *http.Request) (int64, *apierr.APIError) {
	raw := strings.TrimSpace(r.PathValue("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apierr.New(apierr.CodeResourceNotFound)
	}
	return id, nil
}

// selfAuditRefusal is the system-level separation-of-duties refusal (PRD
// F20-3). It is a permission answer, not a body error: editing the request
// cannot fix it.
func selfAuditRefusal() *apierr.APIError {
	return apierr.New(apierr.CodePermissionDenied).
		WithHint("审批人不得为申请人：请转交其他审核人处理")
}

// stateConflict refuses a decision the queue row cannot accept.
func stateConflict(format string, args ...any) *apierr.APIError {
	return apierr.Newf(apierr.CodeResourceStateConflict, fmt.Sprintf(format, args...))
}

func (s *Service) loadTask(ctx context.Context, id int64) (reviewTask, *apierr.APIError) {
	var (
		task      reviewTask
		assignee  sql.NullInt64
		note      sql.NullString
		decidedAt sql.NullTime
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, biz_type, biz_id, applicant_id, state, assignee_id,
		       decision_note, decided_at, created_at
		  FROM t_review_task WHERE id = ?`, id).
		Scan(&task.ID, &task.BizType, &task.BizID, &task.ApplicantID, &task.State,
			&assignee, &note, &decidedAt, &task.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return reviewTask{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		s.logError(ctx, "review_task_load_failed: "+err.Error())
		return reviewTask{}, apierr.New(apierr.CodeInternalError)
	}
	if assignee.Valid {
		v := assignee.Int64
		task.AssigneeID = &v
	}
	if note.Valid {
		v := note.String
		task.DecisionNote = &v
	}
	if decidedAt.Valid {
		v := decidedAt.Time.UTC()
		task.DecidedAt = &v
	}
	task.CreatedAt = task.CreatedAt.UTC()
	return task, nil
}

// insertAudit writes one row in the caller's transaction. module "REVIEW"
// keeps queue decisions separable from the business row's own trail in
// t_admin_audit_log.
func (s *Service) insertAudit(ctx context.Context, q audit.Execer, r *http.Request,
	operatorID int64, operatorRole, action string, task reviewTask,
	before, after map[string]any, reason string) error {
	beforeJSON, afterJSON, err := audit.MarshalBeforeAfter(before, after)
	if err != nil {
		return err
	}
	return s.Audit.Insert(ctx, q, audit.Entry{
		OperatorID:   operatorID,
		OperatorRole: operatorRole,
		OperatorIP:   audit.IPBytes(r),
		Module:       "REVIEW",
		Action:       action,
		TargetType:   "t_review_task",
		TargetID:     strconv.FormatInt(task.ID, 10),
		Before:       beforeJSON,
		After:        afterJSON,
		Reason:       truncateRunes(reason, 255),
		Result:       "OK",
	})
}

// recordRefusal writes the audit row for a refused decision. It is
// best-effort: the refusal itself must still reach the caller if the audit
// insert fails.
func (s *Service) recordRefusal(ctx context.Context, r *http.Request,
	operatorID int64, operatorRole, action string, task reviewTask, reason string) {
	err := s.Audit.Insert(ctx, s.DB, audit.Entry{
		OperatorID:   operatorID,
		OperatorRole: operatorRole,
		OperatorIP:   audit.IPBytes(r),
		Module:       "REVIEW",
		Action:       action,
		TargetType:   "t_review_task",
		TargetID:     strconv.FormatInt(task.ID, 10),
		Reason:       truncateRunes(reason, 255),
		Result:       "REJECTED",
	})
	if err != nil {
		s.logWarn(ctx, "review_refusal_audit_failed: "+err.Error())
	}
}

// decide is the single atomic claim: the UPDATE with state='PENDING' is the
// gate. RowsAffected()==1 means this caller won; a duplicate decision sees 0
// and answers 409 without running any side effect again.
func (s *Service) decide(w http.ResponseWriter, r *http.Request, task reviewTask,
	nextState, note, action string, operatorID int64, operatorRole string) {
	ctx := r.Context()
	now := s.now()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "review_decide_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var noteArg any
	if note != "" {
		noteArg = note
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE t_review_task
		   SET state = ?, decision_note = ?, decided_at = ?
		 WHERE id = ? AND state = 'PENDING'`,
		nextState, noteArg, now, task.ID)
	if err != nil {
		s.logError(ctx, "review_decide_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		mw.WriteError(w, r, stateConflict("审核任务已被处理，请刷新后重试"))
		return
	}

	before := map[string]any{"state": StatePending, "biz_type": task.BizType, "biz_id": task.BizID}
	after := map[string]any{"state": nextState, "biz_type": task.BizType, "biz_id": task.BizID}
	if note != "" {
		after["decision_note"] = note
	}
	if err := s.insertAudit(ctx, tx, r, operatorID, operatorRole, action, task,
		before, after, note); err != nil {
		s.logError(ctx, "review_decide_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "review_decide_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	task.State = nextState
	if note != "" {
		task.DecisionNote = &note
	}
	task.DecidedAt = &now
	item, apiErr := s.enrichOne(ctx, task)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, reviewDetailResponse{Review: item})
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
