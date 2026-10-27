package auditq

import (
	"database/sql"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

const (
	// defaultWindow keeps an unfiltered page bounded. Operators normally pick
	// an explicit day range; seven days matches the console log default.
	defaultWindow = 7 * 24 * time.Hour

	// Column widths from 0003_fund_usage_ops.sql. Rejecting an over-long value
	// at the edge is cheaper than letting MySQL compare a value that cannot
	// match anything.
	maxModuleRunes = 32
	maxActionRunes = 32
	maxResultRunes = 24
)

type listItem struct {
	ID           int64            `json:"id"`
	TraceID      string           `json:"trace_id"`
	OperatorID   string           `json:"operator_id"`
	OperatorRole string           `json:"operator_role"`
	OperatorIP   string           `json:"operator_ip"`
	Module       string           `json:"module"`
	Action       string           `json:"action"`
	TargetType   string           `json:"target_type"`
	TargetID     string           `json:"target_id"`
	Before       jsonx.RawMessage `json:"before"`
	After        jsonx.RawMessage `json:"after"`
	Reason       string           `json:"reason,omitempty"`
	Result       string           `json:"result"`
	CreatedAt    time.Time        `json:"created_at"`
}

type listResponse struct {
	Items    []listItem `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Total    int64      `json:"total"`
	From     time.Time  `json:"from"`
	To       time.Time  `json:"to"`
}

// List implements GET /api/admin/audit-logs (permission audit.read).
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	// The endpoint is defined as newest-first. order=asc is rejected instead of
	// being silently flipped, because a caller paginating in ascending order
	// would otherwise see a page that never advances.
	if page.Order != "desc" {
		mw.WriteError(w, r, mw.InvalidQueryField("order"))
		return
	}

	q := r.URL.Query()
	var operatorID int64
	if raw := strings.TrimSpace(q.Get("operator_id")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			mw.WriteError(w, r, mw.InvalidQueryField("operator_id"))
			return
		}
		operatorID = n
	}
	module, apiErr := filterValue(q.Get("module"), maxModuleRunes, "module")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	action, apiErr := filterValue(q.Get("action"), maxActionRunes, "action")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	result, apiErr := filterValue(q.Get("result"), maxResultRunes, "result")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	from, to, apiErr := s.parseWindow(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	where := []string{"created_at >= ?", "created_at <= ?"}
	args := []any{from, to}
	if operatorID > 0 {
		where = append(where, "operator_id = ?")
		args = append(args, operatorID)
	}
	if module != "" {
		where = append(where, "module = ?")
		args = append(args, module)
	}
	if action != "" {
		where = append(where, "action = ?")
		args = append(args, action)
	}
	if result != "" {
		where = append(where, "result = ?")
		args = append(args, result)
	}
	whereSQL := strings.Join(where, " AND ")

	ctx := r.Context()
	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_admin_audit_log WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "auditq_list_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	// Ordering. idx_aal_created (created_at, id) delivers the whole window as
	// a backward index scan; the operator/module/action equality indexes do the
	// same for their own prefixes because InnoDB appends the primary key to
	// every secondary index. Ordering by created_at is the contract: id DESC
	// only matched it while inserts were strictly monotonic, and a backfilled
	// audit row would silently break that assumption.
	orderBy := "created_at DESC, id DESC"
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, trace_id, operator_id, operator_role, operator_ip,
		       module, action, target_type, target_id,
		       before_json, after_json, reason, result, created_at
		  FROM t_admin_audit_log
		 WHERE `+whereSQL+`
		 ORDER BY `+orderBy+`
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "auditq_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]listItem, 0, page.Size)
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			s.logError(ctx, "auditq_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "auditq_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	_ = mw.WriteJSON(w, http.StatusOK, listResponse{
		Items:    items,
		Page:     page.Number,
		PageSize: page.Size,
		Total:    total,
		From:     from,
		To:       to,
	})
}

func scanItem(rows *sql.Rows) (listItem, error) {
	var (
		it         listItem
		operatorID int64
		ip         []byte
		before     []byte
		after      []byte
		reason     sql.NullString
	)
	if err := rows.Scan(
		&it.ID, &it.TraceID, &operatorID, &it.OperatorRole, &ip,
		&it.Module, &it.Action, &it.TargetType, &it.TargetID,
		&before, &after, &reason, &it.Result, &it.CreatedAt,
	); err != nil {
		return listItem{}, err
	}
	it.OperatorID = strconv.FormatInt(operatorID, 10)
	it.OperatorIP = ipString(ip)
	if len(before) > 0 {
		it.Before = jsonx.RawMessage(before)
	}
	if len(after) > 0 {
		it.After = jsonx.RawMessage(after)
	}
	if reason.Valid {
		it.Reason = reason.String
	}
	return it, nil
}

func ipString(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return ""
	}
	return addr.Unmap().String()
}

func filterValue(raw string, maxRunes int, name string) (string, *apierr.APIError) {
	v := strings.ToUpper(strings.TrimSpace(raw))
	if v == "" {
		return "", nil
	}
	if len([]rune(v)) > maxRunes {
		return "", mw.InvalidQueryField(name)
	}
	return v, nil
}

// parseWindow mirrors the console log window: RFC3339 or YYYY-MM-DD, with a
// date-only upper bound meaning the whole day. Omitting both selects the last
// seven days.
func (s *Service) parseWindow(r *http.Request) (from, to time.Time, apiErr *apierr.APIError) {
	now := s.now().Now().UTC()
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
		if dateOnly {
			v = v.Add(24*time.Hour - time.Millisecond)
		}
		to = v
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, mw.InvalidQueryField("from")
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
