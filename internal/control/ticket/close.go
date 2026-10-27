package ticket

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

type closeRequest struct {
	Reason string `json:"reason"`
}

// Close implements POST /api/console/tickets/{id}/close.
//
// Only the ticket's own account may close it here; the operator's counterpart
// is POST /api/admin/tickets/{id}/state. A closed ticket accepts no further
// reply, which is why closing twice is a 409 rather than an idempotent 200:
// the second call cannot be the action the caller thinks it is.
func (s *Service) Close(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	id, apiErr := pathTicketID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req closeRequest
	if apiErr := decodeOptionalJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
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

		var state string
		err = tx.QueryRowContext(ctx, `
			SELECT state FROM t_ticket WHERE id = ? AND user_id = ? FOR UPDATE`,
			id, uid).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return errTicketMissing
		}
		if err != nil {
			return err
		}
		if state == StateClosed {
			return errTicketClosed
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE t_ticket SET state = ?, updated_at = ?
			 WHERE id = ? AND user_id = ? AND state IN (?, ?)`,
			StateClosed, now, id, uid, StateOpen, StateResponded)
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
		s.logError(ctx, "ticket_close_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	row, apiErr := s.loadUserTicket(ctx, uid, id)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	view, apiErr := s.toView(ctx, row, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, view)
}

// ---------------------------------------------------------------------------
// F19-4 / PRD §10.3 ②: account cancellation is refused while a ticket is open
// ---------------------------------------------------------------------------

// OutstandingTicketsError is returned by CheckAccountCancellable when the
// account still has OPEN or RESPONDED tickets. PRD §10.3 requires the refusal
// to list the ticket numbers and point at the close entry, so the numbers are
// part of the error rather than something the caller re-queries.
type OutstandingTicketsError struct {
	TicketNos []string
}

func (e *OutstandingTicketsError) Error() string {
	return "存在未结工单，无法注销：" + strings.Join(e.TicketNos, "、")
}

// OutstandingTickets lists the account's OPEN/RESPONDED ticket numbers in
// creation order. CLOSED tickets do not block cancellation (§10.3: "进行中的
// 工单" = no OPEN or RESPONDED row).
func (s *Service) OutstandingTickets(ctx context.Context, uid int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT ticket_no FROM t_ticket
		 WHERE user_id = ? AND state IN (?, ?)
		 ORDER BY created_at ASC, id ASC`, uid, StateOpen, StateResponded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			return nil, err
		}
		out = append(out, no)
	}
	return out, rows.Err()
}

// CheckAccountCancellable is the exported pre-flight for the (future)
// cancellation endpoint. nil means the account may proceed; otherwise the
// error is *OutstandingTicketsError and lists every blocking ticket number.
//
// It is read-only. It does not close anything on the user's behalf: PRD §10.3
// requires the user to close or have the tickets resolved, and auto-closing
// here would fabricate "resolved" records.
func (s *Service) CheckAccountCancellable(ctx context.Context, uid int64) error {
	nos, err := s.OutstandingTickets(ctx, uid)
	if err != nil {
		return err
	}
	if len(nos) == 0 {
		return nil
	}
	return &OutstandingTicketsError{TicketNos: nos}
}
