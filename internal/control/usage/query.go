package usage

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// usageRow is both one API item and one CSV record. The query handler and the
// exporter read the same columns through usageColumns, so a header added for
// the page cannot be missing from the file (T14 A4).
type usageRow struct {
	Dimension             string `json:"dimension"`
	ID                    int64  `json:"id"`
	Name                  string `json:"name"`
	RequestTotal          int64  `json:"request_total"`
	RequestSuccess        int64  `json:"request_success"`
	SuccessRateBP         int64  `json:"success_rate_bp"`
	TokensIn              int64  `json:"tokens_in"`
	TokensOut             int64  `json:"tokens_out"`
	ConsumedMicro         int64  `json:"consumed_micro"`
	ConsumedRechargeMicro int64  `json:"consumed_recharge_micro"`
	ConsumedBonusMicro    int64  `json:"consumed_bonus_micro"`
	AmountFen             int64  `json:"amount_fen"`
	AmountYuan            string `json:"amount_yuan"`
}

// exportColumn is the single column definition. Header is what the page and
// the CSV both display; JSON names the same value in the API item, which is
// what lets the front end render the table without a second mapping.
type exportColumn struct {
	Header string
	JSON   string
	Text   func(usageRow) string
}

// usageColumns is the one definition of the usage table's columns. CSV
// writing iterates this slice; Query exposes the headers in the same order in
// the response's "columns" member.
var usageColumns = []exportColumn{
	{"维度", "dimension", func(r usageRow) string { return r.Dimension }},
	{"维度ID", "id", func(r usageRow) string { return strconv.FormatInt(r.ID, 10) }},
	{"名称", "name", func(r usageRow) string { return r.Name }},
	{"请求数", "request_total", func(r usageRow) string { return strconv.FormatInt(r.RequestTotal, 10) }},
	{"成功数", "request_success", func(r usageRow) string { return strconv.FormatInt(r.RequestSuccess, 10) }},
	{"成功率(基点)", "success_rate_bp", func(r usageRow) string { return strconv.FormatInt(r.SuccessRateBP, 10) }},
	{"输入Token", "tokens_in", func(r usageRow) string { return strconv.FormatInt(r.TokensIn, 10) }},
	{"输出Token", "tokens_out", func(r usageRow) string { return strconv.FormatInt(r.TokensOut, 10) }},
	{"消耗微积分", "consumed_micro", func(r usageRow) string { return strconv.FormatInt(r.ConsumedMicro, 10) }},
	{"充值消耗微积分", "consumed_recharge_micro", func(r usageRow) string { return strconv.FormatInt(r.ConsumedRechargeMicro, 10) }},
	{"赠送消耗微积分", "consumed_bonus_micro", func(r usageRow) string { return strconv.FormatInt(r.ConsumedBonusMicro, 10) }},
	{"金额(分)", "amount_fen", func(r usageRow) string { return strconv.FormatInt(r.AmountFen, 10) }},
	{"金额(元)", "amount_yuan", func(r usageRow) string { return r.AmountYuan }},
}

func usageColumnHeaders() []string {
	out := make([]string, len(usageColumns))
	for i, c := range usageColumns {
		out[i] = c.Header
	}
	return out
}

func usageCSVRecord(r usageRow) []string {
	out := make([]string, len(usageColumns))
	for i, c := range usageColumns {
		out[i] = c.Text(r)
	}
	return out
}

// filterView echoes the filters that were actually applied. F9-1's test
// switches dimension and asserts this member is unchanged: the backend
// regroups, the caller's model filter is not dropped.
type filterView struct {
	ModelID   *int64 `json:"model_id"`
	ChannelID *int64 `json:"channel_id"`
}

type usageResponse struct {
	From       time.Time  `json:"from"`
	To         time.Time  `json:"to"`
	Dimension  string     `json:"dimension"`
	Filter     filterView `json:"filter"`
	WindowDays int        `json:"window_days"`
	Columns    []string   `json:"columns"`
	Items      []usageRow `json:"items"`
	Totals     usageRow   `json:"totals"`
}

// Query implements GET /api/console/usage.
func (s *Service) Query(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	f, apiErr := s.parseUsageFilter(r.URL.Query())
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	rows, apiErr := s.fetchUsageRows(r.Context(), uid, f)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	totals := usageRow{Dimension: "total", Name: "合计"}
	for _, row := range rows {
		totals.RequestTotal += row.RequestTotal
		totals.RequestSuccess += row.RequestSuccess
		totals.TokensIn += row.TokensIn
		totals.TokensOut += row.TokensOut
		totals.ConsumedMicro += row.ConsumedMicro
		totals.ConsumedRechargeMicro += row.ConsumedRechargeMicro
		totals.ConsumedBonusMicro += row.ConsumedBonusMicro
	}
	// The total's money is derived from the total micro amount, never from a
	// sum of per-row fen: rounding each row first would drift the total.
	totals.SuccessRateBP = successRateBP(totals.RequestSuccess, totals.RequestTotal)
	totals.AmountFen = fenFromMicro(totals.ConsumedMicro)
	totals.AmountYuan = yuanFromFen(totals.AmountFen)

	_ = mw.WriteJSON(w, http.StatusOK, usageResponse{
		From:       f.From,
		To:         f.To,
		Dimension:  f.Dimension,
		Filter:     filterView{ModelID: f.ModelID, ChannelID: f.ChannelID},
		WindowDays: int(onlineWindow / (24 * time.Hour)),
		Columns:    usageColumnHeaders(),
		Items:      rows,
		Totals:     totals,
	})
}

// fetchUsageRows runs the one aggregation both Query and the CSV exporter
// use. Two layers share one scan so they cannot drift apart:
//
//   - request_total counts every row, including FAILED, and success_rate_bp is
//     derived from it. A rate over success-only rows would always be 10000 and
//     therefore meaningless.
//   - Billing metrics (tokens, consumed_micro and its recharge/bonus split)
//     are SUCCESS-only sums. Hard constraint 6 still holds: a failed request
//     costs nothing and never enters the usage ranking.
//
// The WHERE clause is built once regardless of dimension; switching dimension
// only changes the GROUP BY, which is exactly F9-1.
func (s *Service) fetchUsageRows(ctx context.Context, uid int64, f usageFilter) ([]usageRow, *apierr.APIError) {
	var keyExpr, nameExpr, groupExpr, orderExpr string
	switch f.Dimension {
	case "channel":
		keyExpr = "COALESCE(channel_id, 0)"
		nameExpr = "COALESCE(channel_name_snap, '')"
		groupExpr = "k, n"
		orderExpr = "k ASC"
	case "day":
		// stat_date is a CST natural day everywhere else in the product
		// (t_metric_daily, daily quota). created_at is stored UTC, so the
		// +8h shift defines the same boundary without tzdata.
		keyExpr = "0"
		nameExpr = "DATE_FORMAT(DATE_ADD(created_at, INTERVAL 8 HOUR), '%Y-%m-%d')"
		groupExpr = "n"
		orderExpr = "n ASC"
	default: // model
		keyExpr = "COALESCE(model_id, 0)"
		nameExpr = "model_name_snap"
		groupExpr = "k, n"
		orderExpr = "k ASC"
	}

	where := []string{"user_id = ?", "created_at >= ?", "created_at <= ?"}
	args := []any{uid, f.From, f.To}
	if f.ModelID != nil {
		where = append(where, "model_id = ?")
		args = append(args, *f.ModelID)
	}
	if f.ChannelID != nil {
		where = append(where, "channel_id = ?")
		args = append(args, *f.ChannelID)
	}

	query := `
		SELECT ` + keyExpr + ` AS k, ` + nameExpr + ` AS n,
		       COUNT(*) AS request_total,
		       CAST(COALESCE(SUM(result = 'SUCCESS'), 0) AS SIGNED) AS request_success,
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN tokens_in END), 0) AS SIGNED) AS tokens_in,
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN tokens_out END), 0) AS SIGNED) AS tokens_out,
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN consumed_micro END), 0) AS SIGNED) AS consumed_total,
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN consumed_recharge_micro END), 0) AS SIGNED) AS recharge_total,
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN consumed_bonus_micro END), 0) AS SIGNED) AS bonus_total
		  FROM t_usage_record
		 WHERE ` + strings.Join(where, " AND ") + `
		 GROUP BY ` + groupExpr + `
		 ORDER BY consumed_total DESC, ` + orderExpr

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		s.logError(ctx, "usage_query_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()

	out := make([]usageRow, 0, 8)
	for rows.Next() {
		var row usageRow
		if err := rows.Scan(&row.ID, &row.Name, &row.RequestTotal,
			&row.RequestSuccess, &row.TokensIn, &row.TokensOut, &row.ConsumedMicro,
			&row.ConsumedRechargeMicro, &row.ConsumedBonusMicro); err != nil {
			s.logError(ctx, "usage_query_scan_failed: "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		row.Dimension = f.Dimension
		row.SuccessRateBP = successRateBP(row.RequestSuccess, row.RequestTotal)
		row.AmountFen = fenFromMicro(row.ConsumedMicro)
		row.AmountYuan = yuanFromFen(row.AmountFen)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "usage_query_rows_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	return out, nil
}
