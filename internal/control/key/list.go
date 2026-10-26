package key

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// likeEscape is the LIKE escape character. Using '!' instead of the default
// backslash keeps the query correct on servers configured with
// NO_BACKSLASH_ESCAPES.
const likeEscape = '!'

func likePattern(q string) string {
	replacer := strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	)
	return "%" + replacer.Replace(q) + "%"
}

type listItem struct {
	ID                 int64      `json:"id"`
	KeyName            string     `json:"key_name"`
	KeyMasked          string     `json:"key_masked"`
	Status             string     `json:"status"`
	Expired            bool       `json:"expired"`
	GroupCode          string     `json:"group_code,omitempty"`
	GroupRatioBP       *int64     `json:"group_ratio_bp"`
	ModelScopeMode     string     `json:"model_scope_mode"`
	ModelCount         int        `json:"model_count"`
	IPWhitelistMode    string     `json:"ip_whitelist_mode"`
	IPRuleCount        int        `json:"ip_rule_count"`
	ExpireAt           *time.Time `json:"expire_at"`
	TodayConsumedMicro int64      `json:"today_consumed_micro"`
	CreatedAt          time.Time  `json:"created_at"`
	LastUsedAt         *time.Time `json:"last_used_at"`
}

type listResponse struct {
	Items    []listItem `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Total    int64      `json:"total"`
}

// List implements GET /api/console/keys (F6-1).
//
// Name search and status filter are combined with AND: the acceptance case is
// "five keys, two disabled; search a name fragment + filter disabled returns
// the intersection".
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	page, apiErr := mw.ParsePage(r, "created_at", "last_used_at", "key_name")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 64 {
		mw.WriteError(w, r, mw.InvalidQueryField("q"))
		return
	}
	status, apiErr := parseStatusFilter(r.URL.Query().Get("status"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	where := []string{"k.user_id = ?", "k.deleted_at IS NULL"}
	args := []any{sess.UID}
	if q != "" {
		where = append(where, `k.key_name LIKE ? ESCAPE '!'`)
		args = append(args, likePattern(q))
	}
	if status != "" {
		where = append(where, "k.status = ?")
		args = append(args, status)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM t_api_key k WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(r.Context(), "key_list_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	orderBy := "k.created_at"
	switch page.Sort {
	case "last_used_at":
		orderBy = "k.last_used_at"
	case "key_name":
		orderBy = "k.key_name"
	}
	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}

	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT k.id, k.key_name, k.key_prefix, k.key_last4, k.status, k.group_code,
		       g.ratio_bp, k.model_scope_mode, k.ip_whitelist_mode,
		       k.expire_at, k.last_used_at, k.created_at,
		       (SELECT COUNT(*) FROM t_api_key_model m WHERE m.api_key_id = k.id),
		       (SELECT COUNT(*) FROM t_api_key_ip_rule ir WHERE ir.api_key_id = k.id)
		  FROM t_api_key k
		  LEFT JOIN t_price_group g ON g.code = k.group_code
		 WHERE `+whereSQL+`
		 ORDER BY `+orderBy+` `+direction+`, k.id `+direction+`
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(r.Context(), "key_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	now := s.now()
	items := make([]listItem, 0, page.Size)
	ids := make([]int64, 0, page.Size)
	for rows.Next() {
		var (
			it       listItem
			prefix   string
			last4    string
			rawRatio sql.NullInt64
			expire   sql.NullTime
			lastUsed sql.NullTime
		)
		if err := rows.Scan(&it.ID, &it.KeyName, &prefix, &last4, &it.Status,
			&it.GroupCode, &rawRatio, &it.ModelScopeMode, &it.IPWhitelistMode,
			&expire, &lastUsed, &it.CreatedAt, &it.ModelCount, &it.IPRuleCount); err != nil {
			s.logError(r.Context(), "key_list_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		it.KeyMasked = mask(prefix, last4)
		it.GroupCode, it.GroupRatioBP = visibleGroup(it.GroupCode, rawRatio)
		it.ExpireAt = nullableTime(expire)
		it.LastUsedAt = nullableTime(lastUsed)
		it.Expired = expire.Valid && !expire.Time.After(now)
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "key_list_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	if err := s.fillTodayUsage(r.Context(), sess.UID, now, items, ids); err != nil {
		s.logError(r.Context(), "key_list_usage_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	_ = mw.WriteJSON(w, http.StatusOK, listResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// fillTodayUsage aggregates today's consumed micro-credits for the page in one
// query. A per-row subquery would run 100 scans for page_size=100.
func (s *Service) fillTodayUsage(ctx context.Context, uid int64, now time.Time, items []listItem, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	dayStart := startOfDayCST(now)
	args := make([]any, 0, len(ids)+3)
	args = append(args, uid, dayStart.UTC(), dayStart.Add(24*time.Hour).UTC())
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT api_key_id, COALESCE(SUM(consumed_micro), 0)
		  FROM t_usage_record
		 WHERE user_id = ? AND created_at >= ? AND created_at < ?
		   AND api_key_id IN (`+placeholders(len(ids))+`)
		 GROUP BY api_key_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	usage := make(map[int64]int64, len(ids))
	for rows.Next() {
		var id, micro int64
		if err := rows.Scan(&id, &micro); err != nil {
			return err
		}
		usage[id] = micro
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		items[i].TodayConsumedMicro = usage[items[i].ID]
	}
	return nil
}

func parseStatusFilter(raw string) (string, *apierr.APIError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch strings.ToUpper(raw) {
	case "ENABLED", "ACTIVE":
		return "ENABLED", nil
	case "DISABLED", "INACTIVE":
		return "DISABLED", nil
	default:
		return "", mw.InvalidQueryField("status")
	}
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
