package ticket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// Audit actions. Three operations, three rows — never merged (hard constraint
// 7 / F21-3). Each value stays well inside t_admin_audit_log.result's sibling
// column widths.
const (
	auditActionReply        = "REPLY"
	auditActionStateChange  = "STATE_CHANGE"
	auditActionInternalNote = "INTERNAL_NOTE"
)

type adminReplyRequest struct {
	Content string `json:"content"`
	// IsInternal marks a note that the user must never see. It is a reply row
	// with is_internal = 1, not a reply to the user: no state change, no SLA
	// stop, no e-mail.
	IsInternal bool `json:"is_internal"`
}

type adminReplyResponse struct {
	Reply  replyView       `json:"reply"`
	Ticket adminTicketItem `json:"ticket"`
}

type adminStateRequest struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// Reply implements POST /api/admin/tickets/{id}/reply (F21-1 / F21-3).
//
// A public reply moves OPEN → RESPONDED and stamps first_responded_at only on
// the first reply, which is what stops the SLA clock. An internal note writes
// the reply row but changes neither state nor the clock. Both audit rows carry
// the before/after state and whether the action carried an internal note.
func (s *Service) Reply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, apiErr := pathTicketID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req adminReplyRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	content := normalizeText(req.Content, true)
	if n := len([]rune(content)); n == 0 || n > maxReplyRunes {
		mw.WriteError(w, r, mw.InvalidField("content"))
		return
	}
	now := s.now()

	var (
		replyID    int64
		afterState string
	)
	err := func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		var beforeState string
		err = tx.QueryRowContext(ctx, `
			SELECT state FROM t_ticket WHERE id = ? FOR UPDATE`, id).Scan(&beforeState)
		if errors.Is(err, sql.ErrNoRows) {
			return errTicketMissing
		}
		if err != nil {
			return err
		}
		if beforeState == StateClosed {
			return errTicketClosed
		}

		authorID, _, apiErr := actor(r)
		if apiErr != nil {
			return errors.New("ticket: reply requires a session")
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO t_ticket_reply
				(ticket_id, author_type, author_id, content, is_internal, created_at)
			VALUES (?, 'OPS', ?, ?, ?, ?)`,
			id, authorID, content, req.IsInternal, now)
		if err != nil {
			return err
		}
		replyID, err = res.LastInsertId()
		if err != nil {
			return err
		}

		if req.IsInternal {
			afterState = beforeState
			if _, err := tx.ExecContext(ctx,
				`UPDATE t_ticket SET updated_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
			if err := s.writeAudit(ctx, tx, r, auditActionInternalNote, id,
				beforeState, afterState, true, true, ""); err != nil {
				return err
			}
		} else {
			afterState = StateResponded
			// IFNULL stops the clock only on the first reply: a later reply on an
			// already-RESPONDED ticket must not move first_responded_at.
			res, err = tx.ExecContext(ctx, `
				UPDATE t_ticket
				   SET state = ?, first_responded_at = IFNULL(first_responded_at, ?),
				       last_replied_at = ?, updated_at = ?
				 WHERE id = ? AND state IN (?, ?)`,
				StateResponded, now, now, now, id, StateOpen, StateResponded)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return errTicketClosed
			}
			if err := s.writeAudit(ctx, tx, r, auditActionReply, id,
				beforeState, afterState, false, false, ""); err != nil {
				return err
			}
		}

		// The audit row and the state change commit together: an unlogged
		// operator action must not be committed (F21-3).
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}()
	switch {
	case errors.Is(err, errTicketMissing):
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	case errors.Is(err, errTicketClosed):
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			"工单已关闭，不能回复或添加备注"))
		return
	case err != nil:
		s.logError(ctx, "ticket_reply_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	// The row is committed. E-mail is best-effort: a missing Mailer only warns
	// (hard constraint 5), and a send failure must not turn a delivered reply
	// into a 500.
	if !req.IsInternal {
		s.sendReplyMail(ctx, id)
	}

	view, apiErr := s.adminTicketByID(ctx, id)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, adminReplyResponse{
		Reply: replyView{
			ID:         replyID,
			AuthorType: "OPS",
			Content:    content,
			CreatedAt:  now,
		},
		Ticket: *view,
	})
}

var (
	errTicketMissing = errors.New("ticket: not found")
	errTicketClosed  = errors.New("ticket: closed")
)

// State implements POST /api/admin/tickets/{id}/state.
//
// The only reachable target is CLOSED (PRD §5.9: 运营可代用户关闭). RESPONDED
// is written by Reply, not by hand — setting it here would stop the SLA clock
// with no reply on record, and OPEN would reopen a ticket without the user's
// supplementary message. Anything else is a body error the caller can fix by
// sending CLOSED.
func (s *Service) State(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, apiErr := pathTicketID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req adminStateRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	target := strings.ToUpper(strings.TrimSpace(req.State))
	if target != StateClosed {
		mw.WriteError(w, r, mw.InvalidField("state"))
		return
	}
	reason := truncateRunes(normalizeText(req.Reason, false), maxReasonRunes)
	now := s.now()

	err := func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		var beforeState string
		err = tx.QueryRowContext(ctx, `
			SELECT state FROM t_ticket WHERE id = ? FOR UPDATE`, id).Scan(&beforeState)
		if errors.Is(err, sql.ErrNoRows) {
			return errTicketMissing
		}
		if err != nil {
			return err
		}
		if beforeState == StateClosed {
			return errTicketClosed
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE t_ticket SET state = ?, updated_at = ?
			 WHERE id = ? AND state IN (?, ?)`,
			StateClosed, now, id, StateOpen, StateResponded)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errTicketClosed
		}
		if err := s.writeAudit(ctx, tx, r, auditActionStateChange, id,
			beforeState, StateClosed, false, false, reason); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}()
	switch {
	case errors.Is(err, errTicketMissing):
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	case errors.Is(err, errTicketClosed):
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict, "工单已关闭"))
		return
	case err != nil:
		s.logError(ctx, "ticket_state_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	view, apiErr := s.adminTicketByID(ctx, id)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, view)
}

// sendReplyMail delivers the F21-1 e-mail. Every failure is a WARN: the reply
// already committed and must not be rolled back by a dead SMTP server.
func (s *Service) sendReplyMail(ctx context.Context, ticketID int64) {
	if s.Mailer == nil {
		s.logWarn(ctx, "ticket_reply_mailer_not_configured ticket="+itoa(ticketID))
		return
	}
	var (
		ticketNo, title string
		userID          int64
	)
	if err := s.DB.QueryRowContext(ctx,
		`SELECT ticket_no, title, user_id FROM t_ticket WHERE id = ?`, ticketID).
		Scan(&ticketNo, &title, &userID); err != nil {
		s.logWarn(ctx, "ticket_reply_mail_lookup_failed: "+err.Error())
		return
	}
	var email sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT email FROM t_user WHERE id = ? AND deleted_at IS NULL`, userID).
		Scan(&email); err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logWarn(ctx, "ticket_reply_mail_email_failed: "+err.Error())
		return
	}
	if !email.Valid || strings.TrimSpace(email.String) == "" {
		s.logWarn(ctx, "ticket_reply_mail_no_address ticket="+ticketNo)
		return
	}

	subject := "云枢工单 " + ticketNo + " 已回复"
	body := fmt.Sprintf("您好，您的工单《%s》（%s）已有运营回复，请登录控制台查看。",
		title, ticketNo)
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Mailer.Send(sendCtx, strings.TrimSpace(email.String), subject, body); err != nil {
		s.logWarn(ctx, "ticket_reply_mail_send_failed ticket="+ticketNo+": "+err.Error())
	}
}
