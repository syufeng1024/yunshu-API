package ticket

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// adminReplyView is the operator-only reply shape. The account console's
// visibleReplies filters is_internal in SQL, so the operator detail needs its
// own loader to show internal notes and mark them.
type adminReplyView struct {
	replyView
	IsInternal bool `json:"is_internal"`
}

// adminTicketDetailView reuses ticketView and replaces its user-visible
// replies with the operator set. The outer Replies field is shallower than
// ticketView.Replies, so encoding/json emits the operator list.
type adminTicketDetailView struct {
	ticketView
	UserID    int64            `json:"user_id"`
	UserEmail string           `json:"user_email"`
	Replies   []adminReplyView `json:"replies"`
}

// DetailAdmin implements GET /api/admin/tickets/{id} (permission ticket.reply).
//
// This is an operator read of any user's ticket: there is no user_id predicate,
// and the route permission is the authorization boundary. A missing row and a
// malformed id both answer resource_not_found. The handler never writes the
// ticket state or an audit row.
func (s *Service) DetailAdmin(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathTicketID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	row, apiErr := s.loadAdminTicket(ctx, id)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	view, apiErr := s.toView(ctx, row, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	view.Replies = nil

	replies, apiErr := s.adminReplies(ctx, id)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	email := ""
	err := s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(email, '') FROM t_user WHERE id = ?`, row.UserID).Scan(&email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logWarn(ctx, "ticket_admin_detail_email_failed: "+err.Error())
	}

	_ = mw.WriteJSON(w, http.StatusOK, adminTicketDetailView{
		ticketView: view,
		UserID:     row.UserID,
		UserEmail:  email,
		Replies:    replies,
	})
}

// adminReplies returns every reply, including internal notes. The SQL predicate
// is only ticket_id, so an internal note can never be hidden by a renderer
// forgetting to filter: the operator explicitly sees and labels it.
func (s *Service) adminReplies(ctx context.Context, ticketID int64) ([]adminReplyView, *apierr.APIError) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, author_type, content, created_at, is_internal
		  FROM t_ticket_reply
		 WHERE ticket_id = ?
		 ORDER BY created_at ASC, id ASC`, ticketID)
	if err != nil {
		s.logError(ctx, "ticket_admin_replies_read_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()

	out := make([]adminReplyView, 0, 8)
	for rows.Next() {
		var (
			reply      adminReplyView
			isInternal bool
		)
		if err := rows.Scan(&reply.ID, &reply.AuthorType, &reply.Content,
			&reply.CreatedAt, &isInternal); err != nil {
			s.logError(ctx, "ticket_admin_reply_scan_failed: "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		reply.CreatedAt = reply.CreatedAt.UTC()
		reply.IsInternal = isInternal
		out = append(out, reply)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "ticket_admin_reply_rows_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	return out, nil
}
