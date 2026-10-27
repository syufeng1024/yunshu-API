package logq

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// listItem is one row of GET /api/console/logs. Every display name is a
// snapshot column of t_usage_record: resolving it against t_model or
// t_channel would be the cross-database join T14 forbids.
type listItem struct {
	RequestID        string    `json:"request_id"`
	APIKeyID         int64     `json:"api_key_id"`
	APIKeyName       string    `json:"api_key_name"`
	ModelID          *int64    `json:"model_id"`
	ModelName        string    `json:"model_name"`
	RequestModel     string    `json:"request_model"`
	InboundProtocol  string    `json:"inbound_protocol"`
	ChannelID        *int64    `json:"channel_id"`
	ChannelName      *string   `json:"channel_name"`
	GroupCode        string    `json:"group_code"`
	IsStream         bool      `json:"is_stream"`
	HTTPStatus       int64     `json:"http_status"`
	BizCode          string    `json:"biz_code"`
	Result           string    `json:"result"`
	FailStage        *string   `json:"fail_stage"`
	FailOwner        *string   `json:"fail_owner"`
	TokensIn         int64     `json:"tokens_in"`
	TokensOut        int64     `json:"tokens_out"`
	ConsumedMicro    int64     `json:"consumed_micro"`
	UpstreamAttempts int64     `json:"upstream_attempts"`
	TTFTMs           *int64    `json:"ttft_ms"`
	TotalMs          int64     `json:"total_ms"`
	UpstreamMs       int64     `json:"upstream_ms"`
	ClientIP         string    `json:"client_ip"`
	ClientAborted    bool      `json:"client_aborted"`
	FullLogSaved     bool      `json:"full_log_saved"`
	CreatedAt        time.Time `json:"created_at"`
}

type listResponse struct {
	Items      []listItem `json:"items"`
	Page       int        `json:"page"`
	PageSize   int        `json:"page_size"`
	Total      int64      `json:"total"`
	From       time.Time  `json:"from"`
	To         time.Time  `json:"to"`
	WindowDays int        `json:"window_days"`
}

// parseStateFilter maps the console's state filter onto SQL predicates.
// An empty or "all" value adds no predicate, which is what keeps failed
// requests in the default list (hard constraint 6). Anything else is a query
// error rather than a silently ignored value.
func parseStateFilter(raw string) (clause string, args []any, apiErr *apierr.APIError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return "", nil, nil
	case "success":
		return "result = ?", []any{"SUCCESS"}, nil
	case "failed":
		return "result = ?", []any{"FAILED"}, nil
	case "partial":
		return "result = ?", []any{"PARTIAL"}, nil
	case "rate_limited":
		// 429 is a rate-limit refusal regardless of how settlement classified
		// the row; PRD §5.7 offers it as its own filter.
		return "http_status = ?", []any{int64(http.StatusTooManyRequests)}, nil
	default:
		return "", nil, mw.InvalidQueryField("state")
	}
}

// parseWindow reads from/to. Both accept RFC3339 and the date-only form
// YYYY-MM-DD: from starts at midnight UTC, to ends at 23:59:59.999 UTC.
// Omitting from selects the last seven days.
func (s *Service) parseWindow(r *http.Request) (from, to time.Time, apiErr *apierr.APIError) {
	now := s.now()
	to = now
	from = now.Add(-defaultWindow)

	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get("from")); raw != "" {
		v, _, ok := parseTimestamp(raw)
		if !ok {
			return time.Time{}, time.Time{}, mw.InvalidQueryField("from")
		}
		from = v
	}
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		v, dateOnly, ok := parseTimestamp(raw)
		if !ok {
			return time.Time{}, time.Time{}, mw.InvalidQueryField("to")
		}
		// A date-only upper bound means "the whole day", not midnight. Without
		// this, to=2026-10-27 would drop everything the user did on the 27th.
		if dateOnly {
			v = v.Add(24*time.Hour - time.Millisecond)
		}
		to = v
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, mw.InvalidQueryField("from")
	}
	// The lower bound is the online window. It is checked before the query so
	// the caller learns why the page is not complete instead of receiving an
	// empty result for a range the database never scanned.
	if from.Before(now.Add(-onlineWindow)) {
		e := apierr.New(apierr.CodeInvalidQueryParameter)
		e.Message = "查询范围超出 365 天在线窗口"
		e = e.WithHint("请使用归档导出获取更早的请求日志")
		return time.Time{}, time.Time{}, e
	}
	return from.UTC(), to.UTC(), nil
}

func parseTimestamp(raw string) (t time.Time, dateOnly bool, ok bool) {
	if v, err := time.Parse(time.RFC3339, raw); err == nil {
		return v.UTC(), false, true
	}
	if v, err := time.Parse("2006-01-02", raw); err == nil {
		return v.UTC(), true, true
	}
	return time.Time{}, false, false
}

// List implements GET /api/console/logs.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	page, apiErr := mw.ParsePage(r, "created_at")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	from, to, apiErr := s.parseWindow(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if len([]rune(model)) > maxModelFilterRunes {
		mw.WriteError(w, r, mw.InvalidQueryField("model"))
		return
	}

	// user_id and the created_at range are the first two predicates: they are
	// the definition of idx_ur_user_created, so the range scan starts there.
	where := []string{"user_id = ?", "created_at >= ?", "created_at <= ?"}
	args := []any{uid, from, to}
	if model != "" {
		where = append(where, "model_name_snap = ?")
		args = append(args, model)
	}
	stateClause, stateArgs, apiErr := parseStateFilter(r.URL.Query().Get("state"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if stateClause != "" {
		where = append(where, stateClause)
		args = append(args, stateArgs...)
	}
	whereSQL := strings.Join(where, " AND ")

	ctx := r.Context()
	var total int64
	if err := s.logs().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_usage_record WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "logq_list_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	query := `
		SELECT request_id, api_key_id, api_key_name_snap, model_id,
		       model_name_snap, request_model_req, inbound_protocol,
		       channel_id, channel_name_snap, group_code_snap, is_stream,
		       http_status, biz_code, result, fail_stage, fail_owner,
		       tokens_in, tokens_out, consumed_micro, upstream_attempts,
		       ttft_ms, total_ms, upstream_ms,
		       COALESCE(INET6_NTOA(client_ip), ''), client_aborted,
		       full_log_saved, created_at
		  FROM t_usage_record
		 WHERE ` + whereSQL + `
		 ORDER BY created_at ` + direction + `
		 LIMIT ? OFFSET ?`
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.logs().QueryContext(ctx, query, queryArgs...)
	if err != nil {
		s.logError(ctx, "logq_list_query_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]listItem, 0, page.Size)
	for rows.Next() {
		var (
			v         listItem
			modelID   sql.NullInt64
			channelID sql.NullInt64
			channel   sql.NullString
			failStage sql.NullString
			failOwner sql.NullString
			ttft      sql.NullInt64
		)
		if err := rows.Scan(
			&v.RequestID, &v.APIKeyID, &v.APIKeyName, &modelID,
			&v.ModelName, &v.RequestModel, &v.InboundProtocol,
			&channelID, &channel, &v.GroupCode, &v.IsStream,
			&v.HTTPStatus, &v.BizCode, &v.Result, &failStage, &failOwner,
			&v.TokensIn, &v.TokensOut, &v.ConsumedMicro, &v.UpstreamAttempts,
			&ttft, &v.TotalMs, &v.UpstreamMs,
			&v.ClientIP, &v.ClientAborted, &v.FullLogSaved, &v.CreatedAt,
		); err != nil {
			s.logError(ctx, "logq_list_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		v.ModelID = nullInt64Ptr(modelID)
		v.ChannelID = nullInt64Ptr(channelID)
		v.ChannelName = nullStringPtr(channel)
		v.FailStage = nullStringPtr(failStage)
		v.FailOwner = nullStringPtr(failOwner)
		v.TTFTMs = nullInt64Ptr(ttft)
		v.CreatedAt = v.CreatedAt.UTC()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "logq_list_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	_ = mw.WriteJSON(w, http.StatusOK, listResponse{
		Items:      items,
		Page:       page.Number,
		PageSize:   page.Size,
		Total:      total,
		From:       from,
		To:         to,
		WindowDays: int(onlineWindow / (24 * time.Hour)),
	})
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
