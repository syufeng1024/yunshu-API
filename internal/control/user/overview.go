// Package user implements the console account overview (PRD §5.1 / F5-2).
//
// Every number here comes from a real aggregate. The prototype's chart values
// are not reproduced: an empty account returns zeros and an empty 24-hour
// series, never plausible-looking sample data.
package user

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
	"yunshu.dev/core/internal/control/units"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// Service owns the overview queries.
type Service struct {
	DB     *sql.DB
	Redis  *redisx.Client
	Now    clock.Clock
	Logger *obslog.Logger
}

// New wires the overview service. Redis carries the live K4 daily counter;
// MySQL carries the materialised mirror used as a fallback.
func New(db *sql.DB, rdb *redisx.Client, now clock.Clock, logger *obslog.Logger) *Service {
	if now == nil {
		now = clock.Real{}
	}
	return &Service{DB: db, Redis: rdb, Now: now, Logger: logger}
}

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "control", Status: http.StatusInternalServerError})
	}
}

func (s *Service) logWarn(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Warn(ctx, msg, obslog.Fields{Stage: "control"})
	}
}

// cst is the quota calendar zone (UTC+8). Fixed because China has no DST.
var cst = time.FixedZone("CST", 8*3600)

func startOfDayCST(t time.Time) time.Time {
	local := t.In(cst)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cst)
}

type balanceView struct {
	Micro         int64  `json:"micro"`
	Credits       string `json:"credits"`
	CNY           string `json:"cny"`
	RechargeMicro int64  `json:"recharge_micro"`
	BonusMicro    int64  `json:"bonus_micro"`
}

type todayView struct {
	Requests        int64  `json:"requests"`
	ConsumedMicro   int64  `json:"consumed_micro"`
	ConsumedCredits string `json:"consumed_credits"`
	SuccessRateBP   *int64 `json:"success_rate_bp"`
}

type hourPoint struct {
	Hour          string `json:"hour"`
	Requests      int64  `json:"requests"`
	ConsumedMicro int64  `json:"consumed_micro"`
}

type planView struct {
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	PolicySource  string     `json:"policy_source"`
	TrialExpireAt *time.Time `json:"trial_expire_at"`
}

type quotaView struct {
	RPM             int64  `json:"rpm"`
	TPM             int64  `json:"tpm"`
	Concurrency     int64  `json:"concurrency"`
	DailyQuotaMicro *int64 `json:"daily_quota_micro"`
	UsedTodayMicro  int64  `json:"used_today_micro"`
	RemainingMicro  *int64 `json:"remaining_micro"`
}

type etaView struct {
	Reachable        bool       `json:"reachable"`
	Reason           string     `json:"reason"`
	ETAAt            *time.Time `json:"eta_at"`
	SecondsLeft      *int64     `json:"seconds_left"`
	BurnMicroPerHour int64      `json:"burn_micro_per_hour"`
}

type overviewResponse struct {
	Balance balanceView `json:"balance"`
	Today   todayView   `json:"today"`
	Hourly  []hourPoint `json:"hourly"`
	Plan    planView    `json:"plan"`
	Quota   quotaView   `json:"quota"`
	ETA     etaView     `json:"eta"`
}

// Overview implements GET /api/console/overview.
func (s *Service) Overview(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	ctx := r.Context()
	now := s.Now.Now().UTC()
	dayStart := startOfDayCST(now)

	balance, apiErr := s.balance(ctx, sess.UID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	today, apiErr := s.todayAggregate(ctx, sess.UID, dayStart)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	hourly, apiErr := s.hourlySeries(ctx, sess.UID, now)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ent, err := plan.Resolve(ctx, s.DB, sess.UID, now)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			mw.WriteError(w, r, mw.SessionMissing())
			return
		}
		s.logError(ctx, "overview_plan_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	usedToday := s.quotaUsed(ctx, sess.UID, dayStart)
	lastHour, apiErr := s.lastHourConsumed(ctx, sess.UID, now)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	quota := quotaView{
		RPM:             ent.RPM,
		TPM:             ent.TPM,
		Concurrency:     ent.Concurrency,
		DailyQuotaMicro: ent.DailyQuotaMicro,
		UsedTodayMicro:  usedToday,
	}
	if ent.DailyQuotaMicro != nil {
		remaining := *ent.DailyQuotaMicro - usedToday
		quota.RemainingMicro = &remaining
	}

	_ = mw.WriteJSON(w, http.StatusOK, overviewResponse{
		Balance: balance,
		Today:   today,
		Hourly:  hourly,
		Plan: planView{
			Code:          ent.Code,
			Name:          ent.Name,
			PolicySource:  ent.Source,
			TrialExpireAt: ent.TrialExpireAt,
		},
		Quota: quota,
		ETA:   computeETA(now, ent.DailyQuotaMicro, usedToday, lastHour),
	})
}

func (s *Service) balance(ctx context.Context, uid int64) (balanceView, *apierr.APIError) {
	var (
		recharge, bonus, total int64
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT balance_recharge_micro, balance_bonus_micro, balance_micro
		  FROM t_credit_account WHERE user_id = ?`, uid).Scan(&recharge, &bonus, &total)
	if errors.Is(err, sql.ErrNoRows) {
		// Registration creates the account row; a missing row means the data is
		// incomplete, not that the user has zero. Surface zeros and log it.
		s.logWarn(ctx, "overview_credit_account_missing: user_id="+strconv.FormatInt(uid, 10))
		return balanceView{Micro: 0, Credits: "0", CNY: "0"}, nil
	}
	if err != nil {
		s.logError(ctx, "overview_balance_failed: "+err.Error())
		return balanceView{}, apierr.New(apierr.CodeInternalError)
	}
	return balanceView{
		Micro:         total,
		Credits:       units.Credits(total),
		CNY:           units.CNY(total),
		RechargeMicro: recharge,
		BonusMicro:    bonus,
	}, nil
}

func (s *Service) todayAggregate(ctx context.Context, uid int64, dayStart time.Time) (todayView, *apierr.APIError) {
	var (
		requests, consumed, successes, denominator int64
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(consumed_micro), 0),
		       COALESCE(SUM(CASE WHEN result = 'SUCCESS' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN http_status NOT IN (402, 429) THEN 1 ELSE 0 END), 0)
		  FROM t_usage_record
		 WHERE user_id = ? AND created_at >= ? AND created_at < ?`,
		uid, dayStart.UTC(), dayStart.Add(24*time.Hour).UTC()).
		Scan(&requests, &consumed, &successes, &denominator)
	if err != nil {
		s.logError(ctx, "overview_today_failed: "+err.Error())
		return todayView{}, apierr.New(apierr.CodeInternalError)
	}
	view := todayView{
		Requests:        requests,
		ConsumedMicro:   consumed,
		ConsumedCredits: units.Credits(consumed),
	}
	if denominator > 0 {
		// Internal availability definition (PRD §5.1): 429/402 business
		// rejections are excluded from both numerator and denominator.
		bp := successes * 10000 / denominator
		view.SuccessRateBP = &bp
	}
	return view, nil
}

// hourlySeries returns exactly 24 points ending with the current hour. Hours
// with no traffic are zero-filled so the chart's x-axis is continuous; the
// caller can tell "no requests" from "no data point" by the zero count.
func (s *Service) hourlySeries(ctx context.Context, uid int64, now time.Time) ([]hourPoint, *apierr.APIError) {
	endHour := now.In(cst).Truncate(time.Hour)
	start := endHour.Add(-23 * time.Hour)
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DATE_FORMAT(CONVERT_TZ(created_at, '+00:00', '+08:00'), '%Y-%m-%d %H:00:00'),
		       COUNT(*), COALESCE(SUM(consumed_micro), 0)
		  FROM t_usage_record
		 WHERE user_id = ? AND created_at >= ? AND created_at < ?
		 GROUP BY 1`,
		uid, start.UTC(), endHour.Add(time.Hour).UTC())
	if err != nil {
		s.logError(ctx, "overview_hourly_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()
	type agg struct {
		requests int64
		consumed int64
	}
	byHour := make(map[string]agg, 24)
	for rows.Next() {
		var (
			key string
			a   agg
		)
		if err := rows.Scan(&key, &a.requests, &a.consumed); err != nil {
			s.logError(ctx, "overview_hourly_scan_failed: "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		byHour[key] = a
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "overview_hourly_rows_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	const layout = "2006-01-02 15:00:00"
	out := make([]hourPoint, 0, 24)
	for i := 0; i < 24; i++ {
		hour := start.Add(time.Duration(i) * time.Hour)
		a := byHour[hour.Format(layout)]
		out = append(out, hourPoint{
			Hour:          hour.Format(time.RFC3339),
			Requests:      a.requests,
			ConsumedMicro: a.consumed,
		})
	}
	return out, nil
}

// quotaUsed prefers the live K4 counter: the materialised row is written by
// the daily job and can lag the current request by hours.
func (s *Service) quotaUsed(ctx context.Context, uid int64, dayStart time.Time) int64 {
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
			s.logWarn(ctx, "overview_quota_redis_failed: "+err.Error())
		}
	}
	var used int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT used_total_micro FROM t_user_daily_quota
		 WHERE user_id = ? AND stat_date = ?`, uid, dayStart.Format("2006-01-02")).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logWarn(ctx, "overview_quota_used_failed: "+err.Error())
	}
	return used
}

func (s *Service) lastHourConsumed(ctx context.Context, uid int64, now time.Time) (int64, *apierr.APIError) {
	var used int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(consumed_micro), 0) FROM t_usage_record
		 WHERE user_id = ? AND created_at >= ? AND created_at < ?`,
		uid, now.Add(-time.Hour), now).Scan(&used)
	if err != nil {
		s.logError(ctx, "overview_burn_rate_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	return used, nil
}

// computeETA extrapolates the trailing one-hour burn rate to the daily limit.
//
// remaining / burn is the correct horizon: the daily counter already contains
// today's consumption. A 5,000-credit limit with 500 used and 500/hour takes
// nine more hours, not ten. The card's "about 10 h ±10%" includes this value;
// A11 asserts the arithmetic, not a hard-coded 10.
func computeETA(now time.Time, dailyQuotaMicro *int64, usedMicro, lastHourMicro int64) etaView {
	view := etaView{BurnMicroPerHour: lastHourMicro}
	if dailyQuotaMicro == nil {
		view.Reason = "NO_DAILY_QUOTA"
		return view
	}
	remaining := *dailyQuotaMicro - usedMicro
	if remaining <= 0 {
		view.Reachable = true
		view.Reason = "QUOTA_REACHED"
		zero := int64(0)
		view.SecondsLeft = &zero
		at := now
		view.ETAAt = &at
		return view
	}
	if lastHourMicro <= 0 {
		view.Reason = "NO_RECENT_USAGE"
		return view
	}
	seconds := (remaining*3600 + lastHourMicro - 1) / lastHourMicro
	view.Reachable = true
	view.Reason = "OK"
	view.SecondsLeft = &seconds
	at := now.Add(time.Duration(seconds) * time.Second)
	view.ETAAt = &at
	return view
}
