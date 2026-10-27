package fund

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/ledger"
)

// t_quota_apply.state — the four DDL values (TDS §1.5).
const (
	QuotaStatePending  = "PENDING"
	QuotaStateApproved = "APPROVED"
	QuotaStateRejected = "REJECTED"
	QuotaStateExpired  = "EXPIRED"
)

var quotaStates = []string{
	QuotaStatePending,
	QuotaStateApproved,
	QuotaStateRejected,
	QuotaStateExpired,
}

const (
	// quotaMaxMultiplier is B-24: a single request may not exceed 5x the
	// current daily quota.
	quotaMaxMultiplier int64 = 5
	// quotaDefaultDays is the default validity of an approved override.
	quotaDefaultDays = 30
)

// utc8 is the business timezone for "next day 00:00" (B-24).
var utc8 = time.FixedZone("UTC+8", 8*60*60)

// ErrQuotaState means the requested transition is not reachable.
var ErrQuotaState = errors.New("fund: quota apply state does not allow this transition")

type createQuotaRequest struct {
	TargetQuotaMicro int64  `json:"target_quota_micro"`
	ApplyReason      string `json:"apply_reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type quotaApplyView struct {
	ID                 int64      `json:"id"`
	ApplyNo            string     `json:"apply_no"`
	CurrentQuotaMicro  int64      `json:"current_quota_micro"`
	CurrentQuota       int64      `json:"current_quota"`
	UsedAtApplyMicro   int64      `json:"used_at_apply_micro"`
	UsedAtApply        int64      `json:"used_at_apply"`
	TargetQuotaMicro   int64      `json:"target_quota_micro"`
	TargetQuota        int64      `json:"target_quota"`
	ApprovedQuotaMicro *int64     `json:"approved_quota_micro"`
	ApprovedQuota      *int64     `json:"approved_quota"`
	ApplyReason        string     `json:"apply_reason"`
	State              string     `json:"state"`
	EffectiveFrom      *time.Time `json:"effective_from"`
	EffectiveTo        *time.Time `json:"effective_to"`
	RejectReason       *string    `json:"reject_reason"`
	AuditorID          *int64     `json:"auditor_id"`
	AuditedAt          *time.Time `json:"audited_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

type createQuotaResponse struct {
	Apply    quotaApplyView `json:"apply"`
	Replayed bool           `json:"idempotent_replay"`
}

type quotaListResponse struct {
	Items    []quotaApplyView `json:"items"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Total    int64            `json:"total"`
}

const quotaColumns = `
	id, apply_no, current_quota_micro, used_at_apply_micro, target_quota_micro,
	apply_reason, state, approved_quota_micro, effective_from, effective_to,
	reject_reason, auditor_id, audited_at, created_at`

// CreateQuotaApply implements POST /api/console/quota-applies (G-08 / F5-1).
// Approval and effect are separate, exported methods owned by T16.
func (s *Service) CreateQuotaApply(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req createQuotaRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	if req.TargetQuotaMicro <= 0 {
		mw.WriteError(w, r, mw.InvalidField("target_quota_micro"))
		return
	}
	reason := strings.TrimSpace(req.ApplyReason)
	if reason == "" || len([]rune(reason)) > 500 {
		mw.WriteError(w, r, mw.InvalidField("apply_reason"))
		return
	}
	idemKey, apiErr := sanitizeIdemKey(r.Header.Get("Idempotency-Key"), req.IdempotencyKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	current, quotaSource, apiErr := s.currentDailyQuota(ctx, uid)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if !quotaSource.limited {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			"当前套餐没有日额度上限，无需申请提额"))
		return
	}
	if req.TargetQuotaMicro <= current {
		mw.WriteError(w, r, mw.InvalidField("target_quota_micro"))
		return
	}
	maxQuota := current * quotaMaxMultiplier
	if req.TargetQuotaMicro > maxQuota {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("目标额度超过单次上限 %d 积分（当前日额度 %d 积分的 %d 倍）",
				maxQuota/MicroPerCredit, current/MicroPerCredit, quotaMaxMultiplier)))
		return
	}
	used := s.usedTodayMicro(ctx, uid)

	id, replayed, finalize, release, apiErr := s.reserveIdem(ctx, uid, idemScopeQuota, idemKey)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if replayed {
		view, apiErr := s.quotaApplyByID(ctx, uid, id)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		_ = mw.WriteJSON(w, http.StatusOK, createQuotaResponse{Apply: view, Replayed: true})
		return
	}

	now := s.now()
	applyNO := orderNo(now, randomDigits)
	var applyID int64
	err := func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO t_quota_apply
				(apply_no, user_id, current_quota_micro, used_at_apply_micro,
				 target_quota_micro, apply_reason, state, created_at, updated_at)
			VALUES (?,?,?,?,?,?, 'PENDING', ?,?)`,
			applyNO, uid, current, used, req.TargetQuotaMicro, reason, now, now)
		if err != nil {
			return err
		}
		applyID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if err := insertReviewTaskTx(ctx, tx, reviewBizQuotaApply, applyID, uid, now); err != nil {
			return err
		}
		if err := s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID:   uid,
			OperatorRole: "PERSONAL",
			OperatorIP:   audit.IPBytes(r),
			Module:       "QUOTA",
			Action:       "QUOTA_APPLY",
			TargetType:   "t_quota_apply",
			TargetID:     itoa(applyID),
			After:        mustMarshal(map[string]any{"target_quota_micro": req.TargetQuotaMicro}),
			Result:       "OK",
		}); err != nil {
			s.logWarn(ctx, "fund_quota_audit_failed: "+err.Error())
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}()
	if err != nil || applyID == 0 {
		release()
		s.logError(ctx, "fund_quota_insert_failed: "+errString(err))
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	finalize(applyID)
	view, apiErr := s.quotaApplyByID(ctx, uid, applyID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusCreated, createQuotaResponse{Apply: view})
}

type dailyQuota struct {
	limited bool
}

// currentDailyQuota resolves the USER-scope daily quota in force right now:
// the newest active policy row, else the account's plan row. A NULL daily
// quota means "no ceiling" and therefore no upgrade to apply for.
func (s *Service) currentDailyQuota(ctx context.Context, uid int64) (int64, dailyQuota, *apierr.APIError) {
	var daily sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
		SELECT daily_quota_micro FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ?
		   AND effective_from <= ?
		   AND (effective_to IS NULL OR effective_to > ?)
		 ORDER BY effective_from DESC, id DESC LIMIT 1`,
		uid, s.now(), s.now()).Scan(&daily)
	if err == nil {
		if !daily.Valid {
			return 0, dailyQuota{limited: false}, nil
		}
		return daily.Int64, dailyQuota{limited: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "fund_quota_policy_read_failed: "+err.Error())
		return 0, dailyQuota{}, apierr.New(apierr.CodeInternalError)
	}
	// No policy row: fall back to the plan (FREE_DEFAULT when plan_id is NULL).
	err = s.DB.QueryRowContext(ctx, `
		SELECT p.daily_quota_micro
		  FROM t_user u
		  LEFT JOIN t_plan p ON p.id = u.plan_id
		 WHERE u.id = ? AND u.deleted_at IS NULL`, uid).Scan(&daily)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, dailyQuota{}, mw.SessionMissing()
	}
	if err != nil {
		s.logError(ctx, "fund_quota_plan_read_failed: "+err.Error())
		return 0, dailyQuota{}, apierr.New(apierr.CodeInternalError)
	}
	if !daily.Valid {
		return 0, dailyQuota{limited: false}, nil
	}
	return daily.Int64, dailyQuota{limited: true}, nil
}

// usedTodayMicro reads the UTC+8 business day's consumed total.
func (s *Service) usedTodayMicro(ctx context.Context, uid int64) int64 {
	day := s.now().In(utc8).Format("2006-01-02")
	var used int64
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(used_total_micro, 0) FROM t_user_daily_quota
		 WHERE user_id = ? AND stat_date = ?`, uid, day).Scan(&used); err != nil {
		return 0
	}
	return used
}

func (s *Service) quotaApplyByID(ctx context.Context, uid, id int64) (quotaApplyView, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+quotaColumns+`
		  FROM t_quota_apply WHERE id = ? AND user_id = ?`, id, uid)
	v, err := scanQuotaApply(row)
	if errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "fund_quota_missing: id="+itoa(id))
		return quotaApplyView{}, apierr.New(apierr.CodeInternalError)
	}
	if err != nil {
		s.logError(ctx, "fund_quota_read_failed: "+err.Error())
		return quotaApplyView{}, apierr.New(apierr.CodeInternalError)
	}
	return v, nil
}

func scanQuotaApply(sc scanner) (quotaApplyView, error) {
	var (
		v             quotaApplyView
		approved      sql.NullInt64
		effectiveFrom sql.NullTime
		effectiveTo   sql.NullTime
		rejectReason  sql.NullString
		auditorID     sql.NullInt64
		auditedAt     sql.NullTime
	)
	if err := sc.Scan(&v.ID, &v.ApplyNo, &v.CurrentQuotaMicro, &v.UsedAtApplyMicro,
		&v.TargetQuotaMicro, &v.ApplyReason, &v.State, &approved,
		&effectiveFrom, &effectiveTo, &rejectReason, &auditorID, &auditedAt,
		&v.CreatedAt); err != nil {
		return quotaApplyView{}, err
	}
	if approved.Valid {
		v.ApprovedQuotaMicro = &approved.Int64
		c := approved.Int64 / MicroPerCredit
		v.ApprovedQuota = &c
	}
	if effectiveFrom.Valid {
		t := effectiveFrom.Time.UTC()
		v.EffectiveFrom = &t
	}
	if effectiveTo.Valid {
		t := effectiveTo.Time.UTC()
		v.EffectiveTo = &t
	}
	if rejectReason.Valid {
		v.RejectReason = &rejectReason.String
	}
	if auditorID.Valid {
		v.AuditorID = &auditorID.Int64
	}
	if auditedAt.Valid {
		t := auditedAt.Time.UTC()
		v.AuditedAt = &t
	}
	v.CurrentQuota = v.CurrentQuotaMicro / MicroPerCredit
	v.UsedAtApply = v.UsedAtApplyMicro / MicroPerCredit
	v.TargetQuota = v.TargetQuotaMicro / MicroPerCredit
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}

// ListQuotaApplies implements GET /api/console/quota-applies.
func (s *Service) ListQuotaApplies(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	page, apiErr := mw.ParsePage(r, "created_at", "audited_at")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	states, apiErr := parseQuotaStatus(r.URL.Query().Get("status"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	where := []string{"user_id = ?"}
	args := []any{uid}
	if len(states) == 1 {
		where = append(where, "state = ?")
		args = append(args, states[0])
	} else if len(states) > 1 {
		ph := make([]string, len(states))
		for i, st := range states {
			ph[i] = "?"
			args = append(args, st)
		}
		where = append(where, "state IN ("+strings.Join(ph, ",")+")")
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_quota_apply WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "fund_quota_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	orderBy := "created_at"
	if page.Sort == "audited_at" {
		orderBy = "audited_at"
	}
	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `SELECT `+quotaColumns+`
		  FROM t_quota_apply WHERE `+whereSQL+`
		  ORDER BY `+orderBy+` `+direction+`, id `+direction+`
		  LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "fund_quota_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]quotaApplyView, 0, page.Size)
	for rows.Next() {
		v, err := scanQuotaApply(rows)
		if err != nil {
			s.logError(ctx, "fund_quota_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "fund_quota_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, quotaListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

func parseQuotaStatus(raw string) ([]string, *apierr.APIError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return nil, nil
	case "pending":
		return []string{QuotaStatePending}, nil
	case "approved":
		return []string{QuotaStateApproved}, nil
	case "rejected":
		return []string{QuotaStateRejected}, nil
	case "expired":
		return []string{QuotaStateExpired}, nil
	default:
		return nil, mw.InvalidQueryField("status")
	}
}

// ---------------------------------------------------------------------------
// Exported approval + effect (T16 审核中心).
// ---------------------------------------------------------------------------

// nextDayUTC8 returns the next 00:00 in UTC+8, expressed in UTC. B-24 fixed
// the same-day approval rule: an approval today takes effect tomorrow.
func nextDayUTC8(now time.Time) time.Time {
	local := now.In(utc8)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, utc8)
	return start.AddDate(0, 0, 1).UTC()
}

// ApproveQuotaApply moves PENDING -> APPROVED and then applies the policy.
// The applicant cannot be the auditor (A13); the attempt is recorded with
// result REJECTED.
func (s *Service) ApproveQuotaApply(ctx context.Context, applyID, auditorID int64) (quotaApplyView, error) {
	denied := false
	err := s.withQuotaTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var (
			userID int64
			state  string
			target int64
		)
		err := tx.QueryRowContext(ctx, `
			SELECT user_id, state, target_quota_micro FROM t_quota_apply
			 WHERE id = ? FOR UPDATE`, applyID).Scan(&userID, &state, &target)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrQuotaState
		}
		if err != nil {
			return err
		}
		if state != QuotaStatePending {
			return fmt.Errorf("%w: %s -> APPROVED", ErrQuotaState, state)
		}
		now := s.now()
		if userID == auditorID {
			denied = true
			return s.Audit.Insert(ctx, tx, audit.Entry{
				OperatorID: auditorID, OperatorRole: "OPS", Module: "QUOTA",
				Action: "APPROVE", TargetType: "t_quota_apply", TargetID: itoa(applyID),
				Reason: "SELF_AUDIT: 申请人不得自审", Result: "REJECTED",
			})
		}
		from := nextDayUTC8(now)
		to := from.AddDate(0, 0, quotaDefaultDays)
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_quota_apply
			   SET state = 'APPROVED', approved_quota_micro = ?,
			       effective_from = ?, effective_to = ?,
			       auditor_id = ?, audited_at = ?, updated_at = ?
			 WHERE id = ? AND state = 'PENDING'`,
			target, from, to, auditorID, now, now, applyID); err != nil {
			return err
		}
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: auditorID, OperatorRole: "OPS", Module: "QUOTA",
			Action: "APPROVE", TargetType: "t_quota_apply", TargetID: itoa(applyID),
			After:  mustMarshal(map[string]any{"effective_from": from, "effective_to": to}),
			Result: "OK",
		})
	})
	if err != nil {
		return quotaApplyView{}, err
	}
	if denied {
		return quotaApplyView{}, ErrSelfAudit
	}
	if err := s.EffectiveQuotaApply(ctx, applyID); err != nil {
		return quotaApplyView{}, fmt.Errorf("approved but not effective: %w", err)
	}
	return s.quotaApplyByIDAdmin(ctx, applyID)
}

// EffectiveQuotaApply writes the QUOTA_APPLY rate-limit row. It is idempotent
// (a retry after a successful write is a no-op) and republishes the routing
// snapshot, which is what bumps cfg:ver so gateways see the new policy.
func (s *Service) EffectiveQuotaApply(ctx context.Context, applyID int64) error {
	var (
		userID int64
		state  string
		daily  int64
		from   time.Time
		to     time.Time
	)
	err := s.withQuotaTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var (
			approved sql.NullInt64
			fromN    sql.NullTime
			toN      sql.NullTime
		)
		err := tx.QueryRowContext(ctx, `
			SELECT user_id, state, approved_quota_micro, effective_from, effective_to
			  FROM t_quota_apply WHERE id = ? FOR UPDATE`, applyID).
			Scan(&userID, &state, &approved, &fromN, &toN)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrQuotaState
		}
		if err != nil {
			return err
		}
		if state != QuotaStateApproved {
			return fmt.Errorf("%w: %s -> effective", ErrQuotaState, state)
		}
		if !approved.Valid || !fromN.Valid || !toN.Valid {
			return fmt.Errorf("%w: approved row lacks quota or window", ledger.ErrInvalidParam)
		}
		daily = approved.Int64
		from = fromN.Time.UTC()
		to = toN.Time.UTC()

		var exists int64
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM t_rate_limit_policy
			 WHERE scope_type = 'USER' AND scope_id = ? AND source = 'QUOTA_APPLY'
			   AND effective_from = ? LIMIT 1`, userID, from).Scan(&exists)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var (
			fallbackID sql.NullInt64
			rpm, tpm   int64
			conc       int64
		)
		err = tx.QueryRowContext(ctx, `
			SELECT id, rpm, tpm, concurrency_quota FROM t_rate_limit_policy
			 WHERE scope_type = 'USER' AND scope_id = ?
			   AND effective_from <= ?
			   AND (effective_to IS NULL OR effective_to > ?)
			 ORDER BY effective_from DESC, id DESC LIMIT 1 FOR UPDATE`,
			userID, from, from).Scan(&fallbackID, &rpm, &tpm, &conc)
		if errors.Is(err, sql.ErrNoRows) {
			// No policy row in force at the effective instant: borrow the
			// plan's three non-quota dimensions, falling back to FREE_DEFAULT
			// when the account never bought a tier.
			err = tx.QueryRowContext(ctx, `
				SELECT p.rpm, p.tpm, p.concurrency
				  FROM t_user u JOIN t_plan p ON p.id = u.plan_id
				 WHERE u.id = ?`, userID).Scan(&rpm, &tpm, &conc)
			if errors.Is(err, sql.ErrNoRows) {
				err = tx.QueryRowContext(ctx, `
					SELECT rpm, tpm, concurrency FROM t_plan
					 WHERE code = 'FREE_DEFAULT' AND enabled = 1 LIMIT 1`).
					Scan(&rpm, &tpm, &conc)
			}
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var fallbackArg any
		if fallbackID.Valid {
			fallbackArg = fallbackID.Int64
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO t_rate_limit_policy
				(scope_type, scope_id, source, rpm, tpm, concurrency_quota,
				 daily_quota_micro, effective_from, effective_to,
				 fallback_policy_id, created_at, updated_at)
			VALUES ('USER', ?, 'QUOTA_APPLY', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, rpm, tpm, conc, daily, from, to, fallbackArg, now, now); err != nil {
			return err
		}
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: 0, OperatorRole: "SYSTEM", Module: "QUOTA",
			Action: "EFFECTIVE", TargetType: "t_quota_apply", TargetID: itoa(applyID),
			After:  mustMarshal(map[string]any{"daily_quota_micro": daily, "effective_from": from, "effective_to": to}),
			Result: "OK",
		})
	})
	if err != nil {
		return err
	}
	// The routing snapshot carries loadRateLimits; republishing is also the
	// only supported way to advance cfg:ver (T05 hard constraint 7).
	if s.PublishRoutes != nil {
		if err := s.PublishRoutes(ctx); err != nil {
			s.logWarn(ctx, "fund_quota_publish_failed: "+err.Error())
			return err
		}
	}
	return nil
}

// RejectQuotaApply moves PENDING -> REJECTED. The reason is mandatory and is
// echoed to the user (PRD F20-2, G-08 Then.4).
func (s *Service) RejectQuotaApply(ctx context.Context, applyID, auditorID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
		return fmt.Errorf("%w: reject_reason is required (max 255)", ledger.ErrInvalidParam)
	}
	var (
		denied    bool
		notifyUID int64
	)
	err := s.withQuotaTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var (
			userID int64
			state  string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT user_id, state FROM t_quota_apply WHERE id = ? FOR UPDATE`,
			applyID).Scan(&userID, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrQuotaState
		}
		if err != nil {
			return err
		}
		if state != QuotaStatePending {
			return fmt.Errorf("%w: %s -> REJECTED", ErrQuotaState, state)
		}
		now := s.now()
		if userID == auditorID {
			denied = true
			return s.Audit.Insert(ctx, tx, audit.Entry{
				OperatorID: auditorID, OperatorRole: "OPS", Module: "QUOTA",
				Action: "REJECT", TargetType: "t_quota_apply", TargetID: itoa(applyID),
				Reason: "SELF_AUDIT: 申请人不得自审", Result: "REJECTED",
			})
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_quota_apply
			   SET state = 'REJECTED', reject_reason = ?, auditor_id = ?,
			       audited_at = ?, updated_at = ?
			 WHERE id = ? AND state = 'PENDING'`,
			reason, auditorID, now, now, applyID); err != nil {
			return err
		}
		notifyUID = userID
		return s.Audit.Insert(ctx, tx, audit.Entry{
			OperatorID: auditorID, OperatorRole: "OPS", Module: "QUOTA",
			Action: "REJECT", TargetType: "t_quota_apply", TargetID: itoa(applyID),
			Reason: reason, Result: "OK",
		})
	})
	if err != nil {
		return err
	}
	if denied {
		return ErrSelfAudit
	}
	if notifyUID > 0 {
		s.notify(ctx, notifyUID, "QUOTA_AUDIT", "提额申请未通过", reason)
	}
	return nil
}

// withQuotaTx runs fn in a transaction, retrying at most twice when InnoDB
// picks this transaction as a deadlock victim (1213) or a lock wait times out
// (1205). The retry is safe because every fn re-reads the row state under
// FOR UPDATE: a rolled-back attempt left no change behind. Anything else is
// returned unchanged.
func (s *Service) withQuotaTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lastErr = func() error {
			tx, err := s.DB.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("fund: begin quota transaction: %w", err)
			}
			if err := fn(ctx, tx); err != nil {
				_ = tx.Rollback()
				return err
			}
			return tx.Commit()
		}()
		if lastErr == nil || !isRetryableTxError(lastErr) {
			return lastErr
		}
		if attempt+1 < maxAttempts {
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(time.Duration(attempt+1) * 20 * time.Millisecond):
			}
		}
	}
	return lastErr
}

func (s *Service) quotaApplyByIDAdmin(ctx context.Context, id int64) (quotaApplyView, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+quotaColumns+`
		  FROM t_quota_apply WHERE id = ?`, id)
	v, err := scanQuotaApply(row)
	if err != nil {
		return quotaApplyView{}, err
	}
	return v, nil
}
