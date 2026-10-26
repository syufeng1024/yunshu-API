package key

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/control/plan"
	"yunshu.dev/core/internal/redisx"
)

type modelRef struct {
	ID        int64  `json:"id"`
	ModelName string `json:"model_name"`
	Display   string `json:"display_name"`
}

type ipRuleView struct {
	CIDR   string `json:"cidr"`
	Family int    `json:"ip_family"`
	Note   string `json:"note,omitempty"`
}

type realtimeView struct {
	AsOf            time.Time `json:"as_of"`
	Available       bool      `json:"available"`
	RPMUsed         *int64    `json:"rpm_used"`
	RPMLimit        int64     `json:"rpm_limit"`
	TPMUsed         *int64    `json:"tpm_used"`
	TPMLimit        int64     `json:"tpm_limit"`
	ConcurrencyUsed *int64    `json:"concurrency_used"`
	ConcurrencyMax  int64     `json:"concurrency_limit"`
}

type accountQuotaView struct {
	UsedTodayMicro  int64  `json:"used_today_micro"`
	DailyQuotaMicro *int64 `json:"daily_quota_micro"`
	RemainingMicro  *int64 `json:"remaining_micro"`
	PlanCode        string `json:"plan_code"`
	PlanName        string `json:"plan_name"`
	PolicySource    string `json:"policy_source"`
}

type detailResponse struct {
	ID              int64            `json:"id"`
	KeyName         string           `json:"key_name"`
	KeyMasked       string           `json:"key_masked"`
	KeyPrefix       string           `json:"key_prefix"`
	Status          string           `json:"status"`
	Expired         bool             `json:"expired"`
	GroupCode       string           `json:"group_code,omitempty"`
	GroupRatioBP    *int64           `json:"group_ratio_bp"`
	GroupNote       string           `json:"group_note,omitempty"`
	ModelScopeMode  string           `json:"model_scope_mode"`
	Models          []modelRef       `json:"models"`
	IPWhitelistMode string           `json:"ip_whitelist_mode"`
	IPAllowlist     []ipRuleView     `json:"ip_allowlist"`
	ExpireAt        *time.Time       `json:"expire_at"`
	RemainingDays   *int64           `json:"remaining_days"`
	CreatedAt       time.Time        `json:"created_at"`
	LastUsedAt      *time.Time       `json:"last_used_at"`
	Realtime        realtimeView     `json:"realtime"`
	AccountQuota    accountQuotaView `json:"account_quota"`
}

// Detail implements GET /api/console/keys/{id} (F7-1, F7-2, F7-3).
//
// The response never contains the plaintext or the stored hash: the mask is
// assembled from key_prefix + "****" + key_last4. An id owned by someone else
// produces key_not_found, the same as an absent id.
func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	id, apiErr := pathKeyID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	rec, apiErr := s.loadOwnedKey(r.Context(), s.DB, sess.UID, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	now := s.now()
	resp := detailResponse{
		ID:              rec.ID,
		KeyName:         rec.Name,
		KeyMasked:       mask(rec.Prefix, rec.Last4),
		KeyPrefix:       rec.Prefix,
		Status:          rec.Status,
		Expired:         rec.ExpireAt.Valid && !rec.ExpireAt.Time.After(now),
		ModelScopeMode:  rec.ModelScopeMode,
		IPWhitelistMode: rec.IPWhitelistMode,
		ExpireAt:        nullableTime(rec.ExpireAt),
		CreatedAt:       rec.CreatedAt.UTC(),
		LastUsedAt:      nullableTime(rec.LastUsedAt),
	}
	resp.GroupCode, resp.GroupRatioBP = visibleGroup(rec.GroupCode, rec.GroupRatioBP)
	if resp.GroupCode == "contract" {
		resp.GroupNote = "合约价由运营维护，用户侧不可切换"
	}
	if rec.ExpireAt.Valid {
		days := int64(rec.ExpireAt.Time.Sub(now).Hours() / 24)
		if rec.ExpireAt.Time.After(now) && rec.ExpireAt.Time.Sub(now)%(24*time.Hour) != 0 {
			days++
		}
		if days < 0 {
			days = 0
		}
		resp.RemainingDays = &days
	}

	models, err := s.loadKeyModels(r.Context(), rec)
	if err != nil {
		s.logError(r.Context(), "key_detail_models_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	resp.Models = models
	rules, err := s.loadIPRules(r.Context(), s.DB, rec.ID)
	if err != nil {
		s.logError(r.Context(), "key_detail_ip_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	resp.IPAllowlist = make([]ipRuleView, 0, len(rules))
	for _, rule := range rules {
		resp.IPAllowlist = append(resp.IPAllowlist, ipRuleView{CIDR: rule.CIDR, Family: rule.Family, Note: rule.Note})
	}

	ent, err := plan.Resolve(r.Context(), s.DB, sess.UID, now)
	if err != nil {
		s.logError(r.Context(), "key_detail_plan_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	snap := RealtimeSnapshot{}
	if s.RT != nil {
		snap = s.RT.Snapshot(r.Context(), rec.ID)
	}
	resp.Realtime = realtimeView{
		AsOf:            now,
		Available:       snap.Available,
		RPMUsed:         snap.RPM,
		RPMLimit:        ent.RPM,
		TPMUsed:         snap.TPM,
		TPMLimit:        ent.TPM,
		ConcurrencyUsed: snap.Concurrency,
		ConcurrencyMax:  ent.Concurrency,
	}
	used := s.todayQuotaUsage(r.Context(), sess.UID, now)
	resp.AccountQuota = accountQuotaView{
		UsedTodayMicro:  used,
		DailyQuotaMicro: ent.DailyQuotaMicro,
		PlanCode:        ent.Code,
		PlanName:        ent.Name,
		PolicySource:    ent.Source,
	}
	if ent.DailyQuotaMicro != nil {
		remaining := *ent.DailyQuotaMicro - used
		resp.AccountQuota.RemainingMicro = &remaining
	}

	_ = mw.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) loadKeyModels(ctx context.Context, rec keyRecord) ([]modelRef, error) {
	if rec.ModelScopeMode == "ALL" {
		// ALL means every listed model, not an empty list. Returning the
		// enumerated set would go stale as the catalog changes; the console
		// renders "全部模型" from the mode.
		return []modelRef{}, nil
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT m.id, m.model_name, m.display_name
		  FROM t_api_key_model km
		  JOIN t_model m ON m.id = km.model_id
		 WHERE km.api_key_id = ?
		 ORDER BY m.model_name`, rec.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]modelRef, 0, 8)
	for rows.Next() {
		var m modelRef
		if err := rows.Scan(&m.ID, &m.ModelName, &m.Display); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// todayQuotaUsage reads the live K4 counter first and falls back to the
// materialised daily row. A missing counter means the account has consumed
// nothing today; a NULL daily quota means unlimited, which the caller renders
// as null rather than 0.
func (s *Service) todayQuotaUsage(ctx context.Context, uid int64, now time.Time) int64 {
	dayStart := startOfDayCST(now)
	day := dayStart.Format("20060102")
	if s.Redis != nil {
		cctx, cancel := s.Redis.CommandContext(ctx)
		raw, err := s.Redis.Raw().Get(cctx, redisx.DailyQuotaKey(uid, day)).Result()
		cancel()
		if err == nil {
			if n, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil {
				return n
			}
		} else if !errors.Is(err, goredis.Nil) {
			s.logWarn(ctx, "key_detail_quota_redis_failed: "+err.Error())
		}
	}
	var used int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT used_total_micro FROM t_user_daily_quota
		 WHERE user_id = ? AND stat_date = ?`,
		uid, dayStart.Format("2006-01-02")).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logWarn(ctx, "key_detail_quota_usage_failed: "+err.Error())
	}
	return used
}
