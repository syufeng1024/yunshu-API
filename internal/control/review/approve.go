package review

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/fund"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/ledger"
)

// Approve implements POST /api/admin/reviews/{id}/approve.
//
// Order matters: the queue row's current state is checked first (a duplicate
// decision never reaches the business service), then self-audit, then the
// side effect, and only then the atomic PENDING→APPROVED claim. A crash
// between the side effect and the claim is recoverable: the business service
// reports "already in the target state", which this handler treats as success
// and lets the retry complete the claim.
func (s *Service) Approve(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req decisionRequest
	if apiErr := decodeOptionalJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	operatorID, operatorRole, apiErr := actor(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	ctx := r.Context()
	task, apiErr := s.loadTask(ctx, id)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if task.ApplicantID == operatorID {
		s.recordRefusal(ctx, r, operatorID, operatorRole, "APPROVE", task,
			"SELF_AUDIT: 审批人不得为申请人")
		mw.WriteError(w, r, selfAuditRefusal())
		return
	}
	if task.State != StatePending {
		mw.WriteError(w, r, stateConflict("审核任务当前状态为 %s，不能再次审批", task.State))
		return
	}

	// A balance adjustment changes money, so its state change, ledger entry and
	// queue claim must share one transaction. The generic path below applies the
	// side effect first and claims afterwards, which cannot offer that
	// guarantee.
	if task.BizType == BizBalanceAdjust {
		s.approveBalanceAdjust(w, r, task, operatorID, operatorRole,
			truncateRunes(strings.TrimSpace(req.Reason), decisionNoteMax))
		return
	}

	if apiErr := s.applyApproval(ctx, task, operatorID, req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	note := truncateRunes(strings.TrimSpace(req.Reason), decisionNoteMax)
	s.decide(w, r, task, StateApproved, note, "APPROVE", operatorID, operatorRole)
}

// applyApproval dispatches to the owning service. Types without an exported
// transition are refused with a hint instead of writing a second state
// machine here (T16 hard constraint 11).
func (s *Service) applyApproval(ctx context.Context, task reviewTask, operatorID int64, req decisionRequest) *apierr.APIError {
	if s.Fund == nil {
		return apierr.New(apierr.CodeInternalError)
	}
	switch task.BizType {
	case BizQuotaApply:
		// ApproveQuotaApply also writes the QUOTA_APPLY rate-limit row through
		// EffectiveQuotaApply; both belong to T13 and are not duplicated here.
		_, err := s.Fund.ApproveQuotaApply(ctx, task.BizID, operatorID)
		return s.transitionResult(ctx, task, err, "t_quota_apply", fund.QuotaStateApproved, fund.ErrQuotaState)

	case BizRefundApply:
		// Approval moves APPLIED→APPROVED. Paying out is the separate
		// StartRefundPayout/MarkRefundPaid path owned by T13's payment flow.
		err := s.Fund.ApproveRefund(ctx, task.BizID, operatorID)
		return s.transitionResult(ctx, task, err, "t_refund_order", fund.RefundStateApproved, fund.ErrRefundState)

	case BizInvoiceApply:
		url := strings.TrimSpace(req.InvoiceURL)
		if url == "" || len(url) > 512 {
			return mw.InvalidField("invoice_url")
		}
		err := s.Fund.IssueInvoice(ctx, task.BizID, operatorID, url)
		return s.transitionResult(ctx, task, err, "t_invoice_apply", fund.InvoiceStateIssued, fund.ErrInvoiceState)

	case BizRedeemBatch:
		return stateConflict(
			"单据类型 %s 在资金域没有可调用的审批方法；审批中心不直接改业务表", task.BizType).
			WithHint("该类型的提交入口尚未实现，详见 T16 交付回执的「副作用委派」")

	default:
		return stateConflict("未知单据类型 %s", task.BizType)
	}
}

// transitionResult translates the owning service's answer. "Already in the
// target state" is success on a retry; any other lifecycle refusal is a 409;
// unexpected errors are internal.
func (s *Service) transitionResult(ctx context.Context, task reviewTask, err error,
	table, targetState string, stateErr error) *apierr.APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, fund.ErrSelfAudit) {
		return selfAuditRefusal()
	}
	if errors.Is(err, stateErr) {
		var state string
		readErr := s.DB.QueryRowContext(ctx,
			`SELECT state FROM `+table+` WHERE id = ?`, task.BizID).Scan(&state)
		switch {
		case errors.Is(readErr, sql.ErrNoRows):
			return apierr.New(apierr.CodeResourceNotFound)
		case readErr != nil:
			s.logError(ctx, "review_biz_state_failed: "+readErr.Error())
			return apierr.New(apierr.CodeInternalError)
		case state == targetState:
			return nil // a previous attempt already applied the side effect
		default:
			return stateConflict("单据当前状态为 %s，不能变更为 %s", state, targetState)
		}
	}
	if errors.Is(err, ledger.ErrInvalidParam) {
		return stateConflict("资金域拒绝该操作：%s", err.Error())
	}
	s.logError(ctx, "review_approve_failed: "+err.Error())
	return apierr.New(apierr.CodeInternalError)
}
