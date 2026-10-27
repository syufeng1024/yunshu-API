package publicapi

import (
	"database/sql"
	"net/http"
)

// publicPlan mirrors web/src/api/public.ts PublicPlan. invoice_supported and
// permanent are t_plan tinyint(1) flags but the contract types them as 0 | 1,
// so they stay integers on the wire (a bool would serialise as true/false).
type publicPlan struct {
	Code               string `json:"code"`
	Name               string `json:"name"`
	PriceFen           int64  `json:"price_fen"`
	GrantRechargeMicro int64  `json:"grant_recharge_micro"`
	GrantBonusMicro    int64  `json:"grant_bonus_micro"`
	BonusValidDays     int64  `json:"bonus_valid_days"`
	RPM                int64  `json:"rpm"`
	TPM                int64  `json:"tpm"`
	Concurrency        int64  `json:"concurrency"`
	// DailyQuotaMicro is NULL for FREE_DEFAULT ("no ceiling"). The pointer keeps
	// the JSON value null instead of turning it into 0 ("inherit the previous
	// tier"), which is a different product meaning.
	DailyQuotaMicro  *int64 `json:"daily_quota_micro"`
	ModelScopeMode   string `json:"model_scope_mode"`
	InvoiceSupported int64  `json:"invoice_supported"`
	SLAText          string `json:"sla_text"`
	Permanent        int64  `json:"permanent"`
	SortOrder        int64  `json:"sort_order"`
}

type plansResponse struct {
	Data []publicPlan `json:"data"`
}

// Plans implements GET /api/public/plans.
//
// Only enabled rows are published; the ordering is the pricing page's card
// order. A second id key keeps equal sort_order values stable between requests.
func (s *Service) Plans(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT code, name, price_fen, grant_recharge_micro, grant_bonus_micro,
		       bonus_valid_days, rpm, tpm, concurrency, daily_quota_micro,
		       model_scope_mode, invoice_supported, sla_text, permanent, sort_order
		  FROM t_plan
		 WHERE enabled = 1
		 ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		s.fail(w, r, "public_plans_failed: "+err.Error())
		return
	}
	defer rows.Close()

	data := make([]publicPlan, 0, 8)
	for rows.Next() {
		var (
			p     publicPlan
			daily sql.NullInt64
		)
		if err := rows.Scan(&p.Code, &p.Name, &p.PriceFen, &p.GrantRechargeMicro,
			&p.GrantBonusMicro, &p.BonusValidDays, &p.RPM, &p.TPM, &p.Concurrency,
			&daily, &p.ModelScopeMode, &p.InvoiceSupported, &p.SLAText,
			&p.Permanent, &p.SortOrder); err != nil {
			s.fail(w, r, "public_plans_scan_failed: "+err.Error())
			return
		}
		if daily.Valid {
			v := daily.Int64
			p.DailyQuotaMicro = &v
		}
		data = append(data, p)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, "public_plans_rows_failed: "+err.Error())
		return
	}
	s.write(w, plansResponse{Data: data})
}
