package review

import (
	"context"
	"net/http"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/fund"
	"yunshu.dev/core/internal/control/mw"
)

// Reject implements POST /api/admin/reviews/{id}/reject (F20-2).
//
// The reason is validated server-side. A rejection without one is not a
// decision the applicant can act on, and the browser's `required` attribute is
// not a control: an empty reason answers 400 invalid_request_body.
func (s *Service) Reject(w http.ResponseWriter, r *http.Request) {
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
	// Separation of duties is checked before body shape: a self-approver must
	// never get a different answer just because the reason was empty.
	if task.ApplicantID == operatorID {
		s.recordRefusal(ctx, r, operatorID, operatorRole, "REJECT", task,
			"SELF_AUDIT: 审批人不得为申请人")
		mw.WriteError(w, r, selfAuditRefusal())
		return
	}
	if task.State != StatePending {
		mw.WriteError(w, r, stateConflict("审核任务当前状态为 %s，不能再次审批", task.State))
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len([]rune(reason)) > decisionNoteMax {
		mw.WriteError(w, r, mw.InvalidField("reason"))
		return
	}

	// Rejection writes no money, but both the document and the queue row must
	// move in one transaction so a crash cannot leave an APPROVED queue row
	// pointing at a PENDING document.
	if task.BizType == BizBalanceAdjust {
		s.rejectBalanceAdjust(w, r, task, operatorID, operatorRole, reason)
		return
	}

	if apiErr := s.applyRejection(ctx, task, operatorID, reason); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	s.decide(w, r, task, StateRejected, reason, "REJECT", operatorID, operatorRole)
}

func (s *Service) applyRejection(ctx context.Context, task reviewTask, operatorID int64, reason string) *apierr.APIError {
	if s.Fund == nil {
		return apierr.New(apierr.CodeInternalError)
	}
	switch task.BizType {
	case BizQuotaApply:
		err := s.Fund.RejectQuotaApply(ctx, task.BizID, operatorID, reason)
		return s.transitionResult(ctx, task, err, "t_quota_apply", fund.QuotaStateRejected, fund.ErrQuotaState)

	case BizRefundApply:
		// RejectRefund also releases the frozen amount and returns the
		// recharge order to PAID inside T13's transaction.
		err := s.Fund.RejectRefund(ctx, task.BizID, operatorID, reason)
		return s.transitionResult(ctx, task, err, "t_refund_order", fund.RefundStateRejected, fund.ErrRefundState)

	case BizInvoiceApply:
		// fund has no exported RejectInvoice. Writing t_invoice_apply here
		// would create a second writer for the invoice state machine, which
		// TDS §1.9 forbids. Report the gap instead of inventing one.
		return stateConflict("开票申请暂无可调用的驳回方法（资金域未导出 RejectInvoice）").
			WithHint("驳回开票申请需由资金域补充导出方法；见 T16 交付回执「副作用委派」")

	case BizRedeemBatch:
		return stateConflict(
			"单据类型 %s 在资金域没有可调用的驳回方法；审批中心不直接改业务表", task.BizType).
			WithHint("该类型的提交入口尚未实现，详见 T16 交付回执的「副作用委派」")

	default:
		return stateConflict("未知单据类型 %s", task.BizType)
	}
}
