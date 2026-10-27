package ticket

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// ticketRow is one t_ticket row. Description is loaded on detail and create
// responses only; the list query leaves it out so a 100-row page cannot turn
// into a 500 KB payload.
type ticketRow struct {
	ID                 int64
	TicketNo           string
	UserID             int64
	TicketType         string
	Title              string
	Description        string
	RequestID          sql.NullString
	PolicyHitID        sql.NullInt64
	State              string
	FirstResponseDueAt time.Time
	FirstRespondedAt   sql.NullTime
	LastRepliedAt      sql.NullTime
	ReopenCount        int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

const ticketSelectColumns = `
	t.id, t.ticket_no, t.user_id, t.ticket_type, t.title, t.description,
	t.request_id, t.policy_hit_id, t.state, t.first_response_due_at,
	t.first_responded_at, t.last_replied_at, t.reopen_count,
	t.created_at, t.updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTicket(sc rowScanner) (ticketRow, error) {
	var row ticketRow
	err := sc.Scan(
		&row.ID, &row.TicketNo, &row.UserID, &row.TicketType, &row.Title,
		&row.Description, &row.RequestID, &row.PolicyHitID, &row.State,
		&row.FirstResponseDueAt, &row.FirstRespondedAt, &row.LastRepliedAt,
		&row.ReopenCount, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		return ticketRow{}, err
	}
	row.FirstResponseDueAt = row.FirstResponseDueAt.UTC()
	row.CreatedAt = row.CreatedAt.UTC()
	row.UpdatedAt = row.UpdatedAt.UTC()
	return row, nil
}

func nullTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func nullStringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// ---------------------------------------------------------------------------
// response shapes
// ---------------------------------------------------------------------------

type replyView struct {
	ID         int64     `json:"id"`
	AuthorType string    `json:"author_type"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}

// ticketView is the account-facing detail. It never carries is_internal:
// internal notes are filtered in SQL, not hidden by the renderer.
type ticketView struct {
	ID                 int64       `json:"id"`
	TicketNo           string      `json:"ticket_no"`
	TicketType         string      `json:"ticket_type"`
	Title              string      `json:"title"`
	Description        string      `json:"description"`
	RequestID          *string     `json:"request_id"`
	PolicyHitID        *int64      `json:"policy_hit_id"`
	PolicyRuleID       *string     `json:"policy_rule_id"`
	PolicyCategory     *string     `json:"policy_category"`
	State              string      `json:"state"`
	FirstResponseDueAt time.Time   `json:"first_response_due_at"`
	FirstRespondedAt   *time.Time  `json:"first_responded_at"`
	LastRepliedAt      *time.Time  `json:"last_replied_at"`
	ReopenCount        int64       `json:"reopen_count"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
	SLA                slaView     `json:"sla"`
	Replies            []replyView `json:"replies"`
}

type ticketListItem struct {
	ID                 int64      `json:"id"`
	TicketNo           string     `json:"ticket_no"`
	TicketType         string     `json:"ticket_type"`
	Title              string     `json:"title"`
	State              string     `json:"state"`
	FirstResponseDueAt time.Time  `json:"first_response_due_at"`
	FirstRespondedAt   *time.Time `json:"first_responded_at"`
	LastRepliedAt      *time.Time `json:"last_replied_at"`
	CreatedAt          time.Time  `json:"created_at"`
	SLA                slaView    `json:"sla"`
}

type ticketListResponse struct {
	Items    []ticketListItem `json:"items"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Total    int64            `json:"total"`
}

type createTicketRequest struct {
	TicketType  string `json:"ticket_type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RequestID   string `json:"request_id"`
	// PolicyHitID is the t_content_policy_hit row id, not a rule identifier.
	// A rule id is never accepted from the user: it is derived from the row.
	PolicyHitID *int64 `json:"policy_hit_id"`
}

type createTicketResponse struct {
	Ticket          ticketView `json:"ticket"`
	RequestIDSource string     `json:"request_id_source"`
	Notice          string     `json:"notice,omitempty"`
}

// ---------------------------------------------------------------------------
// filters
// ---------------------------------------------------------------------------

// parseStateFilter accepts the three states plus empty/"all". Anything else —
// including any invented fourth, auto-closed value — is a query error, never
// silently dropped.
func parseStateFilter(raw string) (string, *apierr.APIError) {
	switch v := strings.ToUpper(strings.TrimSpace(raw)); v {
	case "", "ALL":
		return "", nil
	case StateOpen, StateResponded, StateClosed:
		return v, nil
	default:
		return "", mw.InvalidQueryField("state")
	}
}

func parseTypeFilter(raw string) (string, *apierr.APIError) {
	switch v := strings.ToUpper(strings.TrimSpace(raw)); v {
	case "", "ALL":
		return "", nil
	case TypeBilling, TypeTech, TypeContentAppeal, TypeOther:
		return v, nil
	default:
		return "", mw.InvalidQueryField("type")
	}
}

func validType(v string) bool {
	switch v {
	case TypeBilling, TypeTech, TypeContentAppeal, TypeOther:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// loaders
// ---------------------------------------------------------------------------

// loadUserTicket is the only read path for a single ticket on the console.
// user_id is part of the WHERE clause: another account's id answers
// resource_not_found exactly like an absent id (hard constraint 8).
func (s *Service) loadUserTicket(ctx context.Context, uid, id int64) (ticketRow, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+ticketSelectColumns+`
		  FROM t_ticket t WHERE t.id = ? AND t.user_id = ?`, id, uid)
	rec, err := scanTicket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ticketRow{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		s.logError(ctx, "ticket_detail_read_failed: "+err.Error())
		return ticketRow{}, apierr.New(apierr.CodeInternalError)
	}
	return rec, nil
}

// loadAdminTicket is the operator-side row lookup. The operator is allowed to
// see every account's tickets, so there is no user_id predicate.
func (s *Service) loadAdminTicket(ctx context.Context, id int64) (ticketRow, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+ticketSelectColumns+`
		  FROM t_ticket t WHERE t.id = ?`, id)
	rec, err := scanTicket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ticketRow{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		s.logError(ctx, "ticket_admin_read_failed: "+err.Error())
		return ticketRow{}, apierr.New(apierr.CodeInternalError)
	}
	return rec, nil
}

// visibleReplies returns only user-visible replies. is_internal = 0 is in the
// SQL predicate: an internal note is never fetched into the console response,
// so a later refactor cannot leak it by forgetting to filter in the renderer.
func (s *Service) visibleReplies(ctx context.Context, ticketID int64) ([]replyView, *apierr.APIError) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, author_type, content, created_at
		  FROM t_ticket_reply
		 WHERE ticket_id = ? AND is_internal = 0
		 ORDER BY created_at ASC, id ASC`, ticketID)
	if err != nil {
		s.logError(ctx, "ticket_replies_read_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()
	out := make([]replyView, 0, 8)
	for rows.Next() {
		var v replyView
		if err := rows.Scan(&v.ID, &v.AuthorType, &v.Content, &v.CreatedAt); err != nil {
			s.logError(ctx, "ticket_reply_scan_failed: "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "ticket_reply_rows_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	return out, nil
}

// policyRule reads the rule id and category derived from the linked hit. It is
// best-effort: a missing hit row (deleted after retention) must not turn a
// ticket detail into a 500, because the ticket itself still exists.
func (s *Service) policyRule(ctx context.Context, hitID int64) (*string, *string) {
	var ruleID, category string
	err := s.DB.QueryRowContext(ctx,
		`SELECT rule_id, category FROM t_content_policy_hit WHERE id = ?`, hitID).
		Scan(&ruleID, &category)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.logWarn(ctx, "ticket_policy_rule_read_failed: "+err.Error())
		}
		return nil, nil
	}
	return &ruleID, &category
}

// toView renders one row for the account console. A failure to load the
// visible replies is returned, not swallowed: an empty reply list is a valid
// answer, so silently degrading to one would hide a broken detail page.
func (s *Service) toView(ctx context.Context, row ticketRow, withReplies bool) (ticketView, *apierr.APIError) {
	now := s.now()
	respondedAt := nullTimePtr(row.FirstRespondedAt)
	view := ticketView{
		ID:                 row.ID,
		TicketNo:           row.TicketNo,
		TicketType:         row.TicketType,
		Title:              row.Title,
		Description:        row.Description,
		RequestID:          nullStringPtr(row.RequestID),
		PolicyHitID:        nullInt64Ptr(row.PolicyHitID),
		State:              row.State,
		FirstResponseDueAt: row.FirstResponseDueAt,
		FirstRespondedAt:   respondedAt,
		LastRepliedAt:      nullTimePtr(row.LastRepliedAt),
		ReopenCount:        row.ReopenCount,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		SLA:                buildSLA(row.CreatedAt, row.FirstResponseDueAt, respondedAt, row.State, now),
		Replies:            []replyView{},
	}
	if row.PolicyHitID.Valid {
		view.PolicyRuleID, view.PolicyCategory = s.policyRule(ctx, row.PolicyHitID.Int64)
	}
	if withReplies {
		replies, apiErr := s.visibleReplies(ctx, row.ID)
		if apiErr != nil {
			return ticketView{}, apiErr
		}
		view.Replies = replies
	}
	return view, nil
}

func (s *Service) toListItem(row ticketRow, now time.Time) ticketListItem {
	return ticketListItem{
		ID:                 row.ID,
		TicketNo:           row.TicketNo,
		TicketType:         row.TicketType,
		Title:              row.Title,
		State:              row.State,
		FirstResponseDueAt: row.FirstResponseDueAt,
		FirstRespondedAt:   nullTimePtr(row.FirstRespondedAt),
		LastRepliedAt:      nullTimePtr(row.LastRepliedAt),
		CreatedAt:          row.CreatedAt,
		SLA:                buildSLA(row.CreatedAt, row.FirstResponseDueAt, nullTimePtr(row.FirstRespondedAt), row.State, now),
	}
}

// ---------------------------------------------------------------------------
// GET /api/console/tickets
// ---------------------------------------------------------------------------

// List implements the account's ticket list with state and type filters
// (F19-2). Every row is scoped to the session uid.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	page, apiErr := mw.ParsePage(r, "created_at")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	state, apiErr := parseStateFilter(r.URL.Query().Get("state"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	typ, apiErr := parseTypeFilter(r.URL.Query().Get("type"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	where := []string{"user_id = ?"}
	args := []any{uid}
	if state != "" {
		where = append(where, "state = ?")
		args = append(args, state)
	}
	if typ != "" {
		where = append(where, "ticket_type = ?")
		args = append(args, typ)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_ticket WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "ticket_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `SELECT `+ticketSelectColumns+`
		  FROM t_ticket t WHERE `+whereSQL+`
		  ORDER BY t.created_at `+direction+`, t.id `+direction+`
		  LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "ticket_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	now := s.now()
	items := make([]ticketListItem, 0, page.Size)
	for rows.Next() {
		row, err := scanTicket(rows)
		if err != nil {
			s.logError(ctx, "ticket_list_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, s.toListItem(row, now))
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "ticket_list_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, ticketListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// ---------------------------------------------------------------------------
// GET /api/console/tickets/{id}
// ---------------------------------------------------------------------------

// Detail implements the account ticket detail (F19-2). The SLA block renders
// "已响应 / 剩余 xx"; internal notes are absent because visibleReplies filters
// them in SQL.
func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
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
