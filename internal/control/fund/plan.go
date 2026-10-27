package fund

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/control/mw"
)

// freeDefaultCode is the t_plan row that represents "never bought a plan".
// The value is a data identifier, not a business threshold: it is the same
// constant internal/control/plan uses, and it is seeded by
// 0009_seed_plan.sql.
const freeDefaultCode = "FREE_DEFAULT"

// planView is one tier. Every number comes from t_plan, so an operations
// price change reaches the console without a deploy. Amounts are returned in
// both units with their names spelled out: _fen is money, _micro is credits,
// and the plain fields are whole 积分 for display.
type planView struct {
	ID                 int64  `json:"id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	PriceFen           int64  `json:"price_fen"`
	PriceYuan          int64  `json:"price_yuan"`
	GrantRechargeMicro int64  `json:"grant_recharge_micro"`
	GrantBonusMicro    int64  `json:"grant_bonus_micro"`
	GrantRecharge      int64  `json:"grant_recharge"`
	GrantBonus         int64  `json:"grant_bonus"`
	BonusValidDays     int64  `json:"bonus_valid_days"`
	RPM                int64  `json:"rpm"`
	TPM                int64  `json:"tpm"`
	Concurrency        int64  `json:"concurrency"`
	DailyQuotaMicro    *int64 `json:"daily_quota_micro"`
	DailyQuota         *int64 `json:"daily_quota"`
	Permanent          bool   `json:"permanent"`
	Purchasable        bool   `json:"purchasable"`
	SortOrder          int64  `json:"sort_order"`
}

type planListResponse struct {
	Items []planView `json:"items"`
	// DefaultTier is the identity of the tier an account without a plan gets.
	// DefaultTierLabel is the operator-facing label from plan.default_tier.
	DefaultTier      string `json:"default_tier"`
	DefaultTierLabel string `json:"default_tier_label"`
	TrialDays        int64  `json:"trial_days"`
	BonusExpireDays  int64  `json:"bonus_expire_days"`
	MicroPerCredit   int64  `json:"micro_per_credit"`
}

// Plans implements GET /api/console/plans (PRD §3.2.1 / §5.6 套餐购买).
//
// The list is every enabled t_plan row in display order. A tier is
// purchasable when it has a non-zero price: the free default and the trial
// tier are granted, not bought (T13 hard constraint 8).
func (s *Service) Plans(w http.ResponseWriter, r *http.Request) {
	if _, apiErr := sessionUID(r); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	items, apiErr := s.loadPlans(ctx)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, planListResponse{
		Items:            items,
		DefaultTier:      freeDefaultCode,
		DefaultTierLabel: s.configString(ctx, config.KeyPlanDefaultTier, freeDefaultCode),
		TrialDays:        s.configInt(ctx, config.KeyPlanTrialDays, 0),
		BonusExpireDays:  s.bonusExpireDays(ctx, 0),
		MicroPerCredit:   MicroPerCredit,
	})
}

const planColumns = `
	id, code, name, price_fen, grant_recharge_micro, grant_bonus_micro,
	bonus_valid_days, rpm, tpm, concurrency, daily_quota_micro,
	permanent, sort_order`

// loadPlans reads the tier table once. Order creation uses the same reader,
// so a purchase can never be priced differently from what was displayed.
func (s *Service) loadPlans(ctx context.Context) ([]planView, *apierr.APIError) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+planColumns+`
		  FROM t_plan WHERE enabled = 1 ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		s.logError(ctx, "fund_plan_list_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()
	out := make([]planView, 0, 5)
	for rows.Next() {
		v, err := scanPlan(rows)
		if err != nil {
			s.logError(ctx, "fund_plan_scan_failed: "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "fund_plan_rows_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	return out, nil
}

// findPlan loads one enabled tier by t_plan.id. A missing or disabled row is
// reported as a body problem: the client named a plan that cannot be bought,
// and choosing another one fixes the request.
func (s *Service) findPlan(ctx context.Context, id int64) (planView, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+planColumns+`
		  FROM t_plan WHERE id = ? AND enabled = 1`, id)
	v, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return planView{}, mw.InvalidField("plan_id")
	}
	if err != nil {
		s.logError(ctx, "fund_plan_lookup_failed: "+err.Error())
		return planView{}, apierr.New(apierr.CodeInternalError)
	}
	return v, nil
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanPlan(sc scanner) (planView, error) {
	var (
		v     planView
		daily sql.NullInt64
		perm  int64
	)
	if err := sc.Scan(&v.ID, &v.Code, &v.Name, &v.PriceFen, &v.GrantRechargeMicro,
		&v.GrantBonusMicro, &v.BonusValidDays, &v.RPM, &v.TPM, &v.Concurrency,
		&daily, &perm, &v.SortOrder); err != nil {
		return planView{}, err
	}
	v.PriceYuan = v.PriceFen / FenPerYuan
	v.GrantRecharge = microToFen(v.GrantRechargeMicro) / FenPerYuan
	v.GrantBonus = microToFen(v.GrantBonusMicro) / FenPerYuan
	if daily.Valid {
		d := daily.Int64
		v.DailyQuotaMicro = &d
		credits := microToFen(d) / FenPerYuan
		v.DailyQuota = &credits
	}
	v.Permanent = perm == 1
	v.Purchasable = v.PriceFen > 0
	return v, nil
}
