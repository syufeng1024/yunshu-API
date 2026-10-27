package ticket

import (
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// likeEscape is the LIKE escape character. '!' instead of the default
// backslash keeps the query correct on servers with NO_BACKSLASH_ESCAPES.
const likeEscape = '!'

func likePattern(q string) string {
	replacer := strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	)
	return "%" + replacer.Replace(q) + "%"
}

type requestIDItem struct {
	RequestID  string    `json:"request_id"`
	ModelName  string    `json:"model_name"`
	HTTPStatus int       `json:"http_status"`
	Result     string    `json:"result"`
	CreatedAt  time.Time `json:"created_at"`
}

type requestIDListResponse struct {
	Items    []requestIDItem `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
	Since    time.Time       `json:"since"`
}

// Picker implements GET /api/console/request-ids (F19-1).
//
// The 90-day window and user_id are both SQL predicates, so a request older
// than 90 days or owned by another account is not merely hidden in the
// renderer: it never leaves the database. The same window is re-checked when
// a request_id is submitted, because a picker-only filter would be bypassed by
// posting an id directly.
func (s *Service) Picker(w http.ResponseWriter, r *http.Request) {
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
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > maxSearchRunes {
		mw.WriteError(w, r, mw.InvalidQueryField("q"))
		return
	}

	now := s.now()
	since := now.Add(-pickerWindow)

	where := []string{"user_id = ?", "created_at >= ?"}
	args := []any{uid, since}
	if q != "" {
		where = append(where, `(request_id LIKE ? ESCAPE '!' OR model_name_snap LIKE ? ESCAPE '!')`)
		pat := likePattern(q)
		args = append(args, pat, pat)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_usage_record WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "ticket_picker_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `
		SELECT request_id, model_name_snap, http_status, result, created_at
		  FROM t_usage_record
		 WHERE `+whereSQL+`
		 ORDER BY created_at `+direction+`, id `+direction+`
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "ticket_picker_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]requestIDItem, 0, page.Size)
	for rows.Next() {
		var v requestIDItem
		if err := rows.Scan(&v.RequestID, &v.ModelName, &v.HTTPStatus, &v.Result, &v.CreatedAt); err != nil {
			s.logError(ctx, "ticket_picker_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		v.CreatedAt = v.CreatedAt.UTC()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "ticket_picker_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, requestIDListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total, Since: since,
	})
}
