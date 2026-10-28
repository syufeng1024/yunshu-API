package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
)

const (
	// cancelDisableSource distinguishes a self-service cancellation from an
	// operator ban (OPS_MANUAL) and an automatic risk ban (RISK_AUTO). It is a
	// disable_source value, not a new t_user.status enum value: the account
	// still lands in DISABLED, which every existing guard already understands.
	cancelDisableSource = "SELF_CANCEL"
	cancelDisableReason = "用户主动注销"
)

// outstandingTicketNos is the narrow interface implemented by
// ticket.OutstandingTicketsError. errors.As targets the interface, so the auth
// package never needs a compile-time dependency on the ticket package.
type outstandingTicketNos interface {
	OutstandingTicketNos() []string
}

// Cancel implements POST /api/v1/auth/cancel.
//
// Order: the ticket pre-flight runs first and changes nothing when it refuses;
// then status=DISABLED and the audit row commit together; then every K20/K30
// session key is deleted. A revoke failure answers 503 instead of the success
// status because the old sessions outliving the account is the exact condition
// this endpoint must rule out.
func (s *Service) Cancel(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok || sess == nil {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	ctx := r.Context()

	if s.CancellableCheck != nil {
		if err := s.CancellableCheck(ctx, sess.UID); err != nil {
			var blocked outstandingTicketNos
			if errors.As(err, &blocked) {
				nos := blocked.OutstandingTicketNos()
				mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
					"存在未结工单，无法注销：%s", strings.Join(nos, "、")).
					WithHint("请先关闭工单或等待工单处理完成后再注销"))
				return
			}
			s.errorLog(ctx, "cancel_preflight_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
	}

	now := s.now().Now().UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.errorLog(ctx, "cancel_begin_tx_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `
		UPDATE t_user
		   SET status = 'DISABLED', disable_source = ?, disable_reason = ?,
		       disabled_at = ?, disabled_by = NULL, cancel_apply_at = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL AND status <> 'DISABLED'`,
		cancelDisableSource, cancelDisableReason, now, now, now, sess.UID)
	if err != nil {
		s.errorLog(ctx, "cancel_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		// A session can only reach this handler while the account is usable, so
		// zero rows means a concurrent request already disabled it.
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			"账号已注销或已禁用，不能重复注销"))
		return
	}

	if s.Audit != nil {
		before, after, err := audit.MarshalBeforeAfter(
			map[string]any{"status": sess.Status},
			map[string]any{
				"status":          "DISABLED",
				"disable_source":  cancelDisableSource,
				"cancel_apply_at": now,
			})
		if err != nil {
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError).WithCause(err))
			return
		}
		if err := s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID:   sess.UID,
			OperatorRole: sess.Role,
			OperatorIP:   audit.IPBytes(r),
			Module:       "USER",
			Action:       "CANCEL",
			TargetType:   "t_user",
			TargetID:     strconv.FormatInt(sess.UID, 10),
			Before:       before,
			After:        after,
			Reason:       cancelDisableReason,
			Result:       "OK",
		}); err != nil {
			s.errorLog(ctx, "cancel_audit_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		s.errorLog(ctx, "cancel_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	// K30 is the only uid→sid index; RevokeAll deletes every K20 it names and
	// the set itself in one Lua script, so no sid survives a partial failure.
	if err := s.Sessions.RevokeAll(ctx, sess.UID); err != nil {
		s.errorLog(ctx, "cancel_revoke_all_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable))
		return
	}
	s.Sessions.ClearCookie(w)
	s.Sessions.ClearCSRFCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
