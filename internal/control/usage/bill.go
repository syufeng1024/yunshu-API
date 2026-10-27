package usage

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// monthPattern is strict: "2026-1" and "2026-13" are query errors, not values
// time.Parse would silently normalise into a different month.
var monthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

type billResponse struct {
	Month                 string    `json:"month"`
	PeriodStart           time.Time `json:"period_start"`
	PeriodEnd             time.Time `json:"period_end"`
	RequestTotal          int64     `json:"request_total"`
	RequestSuccess        int64     `json:"request_success"`
	SuccessRateBP         int64     `json:"success_rate_bp"`
	ConsumedMicro         int64     `json:"consumed_micro"`
	ConsumedRechargeMicro int64     `json:"consumed_recharge_micro"`
	ConsumedBonusMicro    int64     `json:"consumed_bonus_micro"`
	AmountFen             int64     `json:"amount_fen"`
	AmountYuan            string    `json:"amount_yuan"`
}

// Bill implements GET /api/console/bills?month=YYYY-MM.
//
// The month is a CST (UTC+8) natural month: period_start/period_end are the
// UTC instants of 00:00 CST on the first day and on the first day of the next
// month. A request at 2026-10-31T23:30 CST belongs to October even though its
// UTC timestamp is 2026-10-31T15:30Z.
//
// Amount display follows A3 exactly: 12,345 credit = 123,450,000 micro →
// amount_fen 12345 → amount_yuan "123.45". The front end must not divide by
// 100 itself; both representations are provided so it never has to.
func (s *Service) Bill(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	now := s.now()
	raw := strings.TrimSpace(r.URL.Query().Get("month"))
	var start time.Time
	if raw == "" {
		local := now.In(cst)
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, cst)
	} else {
		if !monthPattern.MatchString(raw) {
			mw.WriteError(w, r, mw.InvalidQueryField("month"))
			return
		}
		parsed, err := time.Parse("2006-01", raw)
		if err != nil {
			mw.WriteError(w, r, mw.InvalidQueryField("month"))
			return
		}
		start = time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, cst)
	}
	end := start.AddDate(0, 1, 0)

	out, apiErr := s.fetchBill(r.Context(), uid, start, end)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	out.Month = start.Format("2006-01")
	_ = mw.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) fetchBill(ctx context.Context, uid int64, start, end time.Time) (billResponse, *apierr.APIError) {
	var out billResponse
	out.PeriodStart = start.UTC()
	out.PeriodEnd = end.UTC()

	// Two layers in one scan so they cannot drift: request_total counts every
	// row (FAILED included) and feeds success_rate_bp, while the money columns
	// sum SUCCESS rows only — a failed request cannot move a monthly bill (A12).
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       CAST(COALESCE(SUM(result = 'SUCCESS'), 0) AS SIGNED),
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN consumed_micro END), 0) AS SIGNED),
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN consumed_recharge_micro END), 0) AS SIGNED),
		       CAST(COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN consumed_bonus_micro END), 0) AS SIGNED)
		  FROM t_usage_record
		 WHERE user_id = ? AND created_at >= ? AND created_at < ?`,
		uid, out.PeriodStart, out.PeriodEnd,
	).Scan(&out.RequestTotal, &out.RequestSuccess, &out.ConsumedMicro,
		&out.ConsumedRechargeMicro, &out.ConsumedBonusMicro)
	if err != nil {
		s.logError(ctx, "usage_bill_query_failed: "+err.Error())
		return billResponse{}, apierr.New(apierr.CodeInternalError)
	}

	out.SuccessRateBP = successRateBP(out.RequestSuccess, out.RequestTotal)
	out.AmountFen = fenFromMicro(out.ConsumedMicro)
	out.AmountYuan = yuanFromFen(out.AmountFen)

	// Split integrity is an operational invariant: the two funding buckets
	// must reconstruct the total. A drift here means some writer bypassed
	// t_usage_record's split columns; log it loudly instead of hiding it by
	// reporting recharge+bonus as the total.
	if out.ConsumedRechargeMicro+out.ConsumedBonusMicro != out.ConsumedMicro {
		s.logWarn(ctx, "usage_bill_split_mismatch uid="+
			itoa(uid)+" total="+itoa(out.ConsumedMicro)+
			" recharge="+itoa(out.ConsumedRechargeMicro)+
			" bonus="+itoa(out.ConsumedBonusMicro))
	}
	return out, nil
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
