package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

type riskRuleView struct {
	ID            int64            `json:"id"`
	RuleName      string           `json:"rule_name"`
	Metric        string           `json:"metric"`
	Operator      string           `json:"operator"`
	Threshold     int64            `json:"threshold"`
	WindowSeconds int64            `json:"window_seconds"`
	Action        string           `json:"action"`
	ActionParam   jsonx.RawMessage `json:"action_param,omitempty"`
	Scope         string           `json:"scope"`
	Enabled       bool             `json:"enabled"`
	CreatedBy     int64            `json:"created_by"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type riskRuleListResponse struct {
	Items    []riskRuleView `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int64          `json:"total"`
}

type riskRuleDetailResponse struct {
	Rule riskRuleView `json:"rule"`
}

type riskRuleWriteRequest struct {
	RuleName      *string           `json:"rule_name"`
	Metric        *string           `json:"metric"`
	Operator      *string           `json:"operator"`
	Threshold     *int64            `json:"threshold"`
	WindowSeconds *int64            `json:"window_seconds"`
	Action        *string           `json:"action"`
	ActionParam   *jsonx.RawMessage `json:"action_param"`
	Scope         *string           `json:"scope"`
	Enabled       *bool             `json:"enabled"`
}

type riskHitView struct {
	ID           int64     `json:"id"`
	RuleID       int64     `json:"rule_id"`
	RuleName     string    `json:"rule_name"`
	ScopeType    string    `json:"scope_type"`
	ScopeValue   string    `json:"scope_value"`
	UserID       *int64    `json:"user_id,omitempty"`
	MetricValue  int64     `json:"metric_value"`
	Threshold    int64     `json:"threshold"`
	ActionTaken  string    `json:"action_taken"`
	ActionResult string    `json:"action_result"`
	CreatedAt    time.Time `json:"created_at"`
}

type riskHitListResponse struct {
	Items    []riskHitView `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}

type riskScoreRequest struct {
	RiskScore *int64 `json:"risk_score"`
	Reason    string `json:"reason"`
}

type riskScoreResponse struct {
	UserID    int64 `json:"user_id"`
	RiskScore int64 `json:"risk_score"`
	Previous  int64 `json:"previous_risk_score"`
}

const riskRuleSelectColumns = `
	r.id, r.rule_name, r.metric, r.operator, r.threshold, r.window_seconds,
	r.action, r.action_param, r.scope, r.enabled, r.created_by, r.created_at, r.updated_at`

// ListRiskRules implements GET /api/admin/risk/rules.
func (s *Service) ListRiskRules(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r, "created_at", "updated_at", "rule_name", "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := r.URL.Query()
	where := []string{"1 = 1"}
	args := make([]any, 0, 3)
	if raw := strings.TrimSpace(q.Get("action")); raw != "" {
		action := strings.ToUpper(raw)
		if !validRiskAction(action) {
			mw.WriteError(w, r, mw.InvalidQueryField("action"))
			return
		}
		where = append(where, "r.action = ?")
		args = append(args, action)
	}
	if raw := strings.TrimSpace(q.Get("scope")); raw != "" {
		scope := strings.ToUpper(raw)
		if !validRiskScope(scope) {
			mw.WriteError(w, r, mw.InvalidQueryField("scope"))
			return
		}
		where = append(where, "r.scope = ?")
		args = append(args, scope)
	}
	if raw := strings.TrimSpace(q.Get("enabled")); raw != "" {
		if raw != "0" && raw != "1" {
			mw.WriteError(w, r, mw.InvalidQueryField("enabled"))
			return
		}
		where = append(where, "r.enabled = ?")
		args = append(args, raw)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM t_risk_rule r WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(r.Context(), "ops_risk_rule_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	orderBy := "r.created_at"
	switch page.Sort {
	case "updated_at":
		orderBy = "r.updated_at"
	case "rule_name":
		orderBy = "r.rule_name"
	case "id":
		orderBy = "r.id"
	}
	dir := orderDirection(page.Order)
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT `+riskRuleSelectColumns+` FROM t_risk_rule r
		 WHERE `+whereSQL+` ORDER BY `+orderBy+` `+dir+`, r.id `+dir+` LIMIT ? OFFSET ?`,
		queryArgs...)
	if err != nil {
		s.logError(r.Context(), "ops_risk_rule_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]riskRuleView, 0, page.Size)
	for rows.Next() {
		v, err := scanRiskRule(rows)
		if err != nil {
			s.logError(r.Context(), "ops_risk_rule_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_risk_rule_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, riskRuleListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// CreateRiskRule implements POST /api/admin/risk/rules.
func (s *Service) CreateRiskRule(w http.ResponseWriter, r *http.Request) {
	var req riskRuleWriteRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	in, apiErr := validateRiskRule(&req, nil)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_risk_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM t_risk_rule WHERE rule_name = ? LIMIT 1`,
		in.ruleName).Scan(&existing)
	if err == nil {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("规则名 %s 已存在（id=%d）", in.ruleName, existing)))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "ops_risk_name_check_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_risk_rule
			(rule_name, metric, operator, threshold, window_seconds, action,
			 action_param, scope, enabled, created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ruleName, in.metric, in.operator, in.threshold, in.windowSeconds,
		in.action, in.actionParam, in.scope, in.enabled, mustActorID(r), now, now)
	if err != nil {
		s.logError(ctx, "ops_risk_insert_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		s.logError(ctx, "ops_risk_id_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	after, apiErr := loadRiskRuleTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "RISK", "CREATE", "t_risk_rule", id, nil,
		riskRuleAuditView(after), ""); err != nil {
		s.logError(ctx, "ops_risk_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_risk_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusCreated, riskRuleDetailResponse{Rule: after})
}

// UpdateRiskRule implements PATCH /api/admin/risk/rules/{id}.
func (s *Service) UpdateRiskRule(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req riskRuleWriteRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if !hasRiskRulePatch(&req) {
		mw.WriteError(w, r, mw.InvalidField("body"))
		return
	}
	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_risk_patch_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	before, apiErr := loadRiskRuleTx(ctx, tx, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	in, apiErr := validateRiskRule(&req, &before)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if in.ruleName != before.RuleName {
		var owner int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM t_risk_rule WHERE rule_name = ? LIMIT 1`,
			in.ruleName).Scan(&owner)
		if err == nil && owner != id {
			mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
				fmt.Sprintf("规则名 %s 已存在（id=%d）", in.ruleName, owner)))
			return
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.logError(ctx, "ops_risk_name_check_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_risk_rule
		   SET rule_name = ?, metric = ?, operator = ?, threshold = ?,
		       window_seconds = ?, action = ?, action_param = ?, scope = ?,
		       enabled = ?, updated_at = ?
		 WHERE id = ?`,
		in.ruleName, in.metric, in.operator, in.threshold, in.windowSeconds,
		in.action, in.actionParam, in.scope, in.enabled, now, id); err != nil {
		s.logError(ctx, "ops_risk_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	after, apiErr := loadRiskRuleTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "RISK", "UPDATE", "t_risk_rule", id,
		riskRuleAuditView(before), riskRuleAuditView(after), ""); err != nil {
		s.logError(ctx, "ops_risk_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_risk_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusOK, riskRuleDetailResponse{Rule: after})
}

// DeleteRiskRule disables the rule instead of deleting the row: t_risk_hit_log
// references rule_id and historical hits must stay attributable. The response
// says "deleted" to match the console's DELETE verb while the audit row records
// enabled true→false.
func (s *Service) DeleteRiskRule(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_risk_delete_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	before, apiErr := loadRiskRuleTx(ctx, tx, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if before.Enabled {
		if _, err := tx.ExecContext(ctx,
			`UPDATE t_risk_rule SET enabled = 0, updated_at = ? WHERE id = ?`, s.now(), id); err != nil {
			s.logError(ctx, "ops_risk_delete_update_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
	}
	after, apiErr := loadRiskRuleTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "RISK", "DELETE", "t_risk_rule", id,
		riskRuleAuditView(before), riskRuleAuditView(after), ""); err != nil {
		s.logError(ctx, "ops_risk_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_risk_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true, "enabled": false})
}

// ListRiskHits implements GET /api/admin/risk/hits.
func (s *Service) ListRiskHits(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r, "created_at", "id", "metric_value")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := r.URL.Query()
	where := []string{"1 = 1"}
	args := make([]any, 0, 4)
	if raw := strings.TrimSpace(q.Get("rule_id")); raw != "" {
		id, err := parseInt64Query(raw)
		if err != nil || id <= 0 {
			mw.WriteError(w, r, mw.InvalidQueryField("rule_id"))
			return
		}
		where = append(where, "h.rule_id = ?")
		args = append(args, id)
	}
	if raw := strings.TrimSpace(q.Get("user_id")); raw != "" {
		id, err := parseInt64Query(raw)
		if err != nil || id <= 0 {
			mw.WriteError(w, r, mw.InvalidQueryField("user_id"))
			return
		}
		where = append(where, "h.user_id = ?")
		args = append(args, id)
	}
	if raw := strings.ToUpper(strings.TrimSpace(q.Get("scope_type"))); raw != "" {
		if !validRiskScope(raw) {
			mw.WriteError(w, r, mw.InvalidQueryField("scope_type"))
			return
		}
		where = append(where, "h.scope_type = ?")
		args = append(args, raw)
	}
	if raw := strings.ToUpper(strings.TrimSpace(q.Get("action_taken"))); raw != "" {
		if !validRiskAction(raw) {
			mw.WriteError(w, r, mw.InvalidQueryField("action_taken"))
			return
		}
		where = append(where, "h.action_taken = ?")
		args = append(args, raw)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM t_risk_hit_log h WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(r.Context(), "ops_risk_hit_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	orderBy := "h.created_at"
	switch page.Sort {
	case "id":
		orderBy = "h.id"
	case "metric_value":
		orderBy = "h.metric_value"
	}
	dir := orderDirection(page.Order)
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT h.id, h.rule_id, COALESCE(r.rule_name, ''), h.scope_type, h.scope_value,
		       h.user_id, h.metric_value, h.threshold, h.action_taken, h.action_result, h.created_at
		  FROM t_risk_hit_log h LEFT JOIN t_risk_rule r ON r.id = h.rule_id
		 WHERE `+whereSQL+` ORDER BY `+orderBy+` `+dir+`, h.id `+dir+` LIMIT ? OFFSET ?`,
		queryArgs...)
	if err != nil {
		s.logError(r.Context(), "ops_risk_hit_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]riskHitView, 0, page.Size)
	for rows.Next() {
		var (
			v      riskHitView
			userID sql.NullInt64
			at     time.Time
		)
		if err := rows.Scan(&v.ID, &v.RuleID, &v.RuleName, &v.ScopeType, &v.ScopeValue,
			&userID, &v.MetricValue, &v.Threshold, &v.ActionTaken, &v.ActionResult, &at); err != nil {
			s.logError(r.Context(), "ops_risk_hit_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		v.UserID = nullableInt64Ptr(userID)
		v.CreatedAt = at.UTC()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_risk_hit_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, riskHitListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// SetUserRiskScore implements POST /api/admin/risk/users/{id}/score.
// The before/after pair is mandatory for every change, not only high scores:
// a risk score is a business qualification that can disable an account.
func (s *Service) SetUserRiskScore(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req riskScoreRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.RiskScore == nil || *req.RiskScore < 0 || *req.RiskScore > 100 {
		mw.WriteError(w, r, mw.InvalidField("risk_score"))
		return
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_risk_score_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var prev int64
	err = tx.QueryRowContext(ctx,
		`SELECT risk_score FROM t_user WHERE id = ? AND deleted_at IS NULL FOR UPDATE`, id).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		mw.WriteError(w, r, apierr.New(apierr.CodeResourceNotFound))
		return
	}
	if err != nil {
		s.logError(ctx, "ops_risk_score_load_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE t_user SET risk_score = ?, updated_at = ? WHERE id = ?`,
		*req.RiskScore, s.now(), id); err != nil {
		s.logError(ctx, "ops_risk_score_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	before := map[string]any{"user_id": id, "risk_score": prev}
	after := map[string]any{"user_id": id, "risk_score": *req.RiskScore}
	if err := s.insertAudit(ctx, tx, r, "RISK", "UPDATE_RISK_SCORE", "t_user", id,
		before, after, req.Reason); err != nil {
		s.logError(ctx, "ops_risk_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_risk_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	if *req.RiskScore >= 80 {
		s.logWarn(ctx, fmt.Sprintf("ops_risk_high_score user=%d score=%d", id, *req.RiskScore))
	}
	_ = mw.WriteJSON(w, http.StatusOK, riskScoreResponse{
		UserID: id, RiskScore: *req.RiskScore, Previous: prev,
	})
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

type riskRuleInput struct {
	ruleName      string
	metric        string
	operator      string
	threshold     int64
	windowSeconds int64
	action        string
	actionParam   any
	scope         string
	enabled       bool
}

func validateRiskRule(req *riskRuleWriteRequest, cur *riskRuleView) (riskRuleInput, *apierr.APIError) {
	var in riskRuleInput
	if cur != nil {
		in = riskRuleInput{
			ruleName:      cur.RuleName,
			metric:        cur.Metric,
			operator:      cur.Operator,
			threshold:     cur.Threshold,
			windowSeconds: cur.WindowSeconds,
			action:        cur.Action,
			actionParam:   rawOrNil(cur.ActionParam),
			scope:         cur.Scope,
			enabled:       cur.Enabled,
		}
	}
	if req.RuleName != nil {
		in.ruleName = strings.TrimSpace(*req.RuleName)
	}
	if in.ruleName == "" || len([]rune(in.ruleName)) > 64 {
		return in, mw.InvalidField("rule_name")
	}
	if req.Metric != nil {
		in.metric = strings.TrimSpace(*req.Metric)
	}
	if in.metric == "" || len([]rune(in.metric)) > 32 {
		return in, mw.InvalidField("metric")
	}
	if req.Operator != nil {
		in.operator = strings.ToUpper(strings.TrimSpace(*req.Operator))
	}
	switch in.operator {
	case "GTE", "LTE", "EQ", "SUM_GTE":
	default:
		return in, mw.InvalidField("operator")
	}
	if req.Threshold != nil {
		in.threshold = *req.Threshold
	}
	if req.WindowSeconds != nil {
		in.windowSeconds = *req.WindowSeconds
	}
	if in.windowSeconds <= 0 || in.windowSeconds > 30*24*3600 {
		return in, mw.InvalidField("window_seconds")
	}
	if req.Action != nil {
		in.action = strings.ToUpper(strings.TrimSpace(*req.Action))
	}
	if !validRiskAction(in.action) {
		return in, mw.InvalidField("action")
	}
	if req.ActionParam != nil {
		raw := *req.ActionParam
		if len(raw) > 0 && string(raw) != "null" {
			var probe any
			if err := jsonx.Unmarshal(raw, &probe); err != nil {
				return in, mw.InvalidField("action_param")
			}
			in.actionParam = raw
		} else {
			in.actionParam = nil
		}
	}
	if req.Scope != nil {
		in.scope = strings.ToUpper(strings.TrimSpace(*req.Scope))
	}
	if !validRiskScope(in.scope) {
		return in, mw.InvalidField("scope")
	}
	if req.Enabled != nil {
		in.enabled = *req.Enabled
	}
	return in, nil
}

func validRiskAction(v string) bool {
	return v == "ALERT" || v == "THROTTLE" || v == "DISABLE"
}

func validRiskScope(v string) bool {
	return v == "USER" || v == "KEY" || v == "IP" || v == "DEVICE"
}

func hasRiskRulePatch(req *riskRuleWriteRequest) bool {
	return req.RuleName != nil || req.Metric != nil || req.Operator != nil ||
		req.Threshold != nil || req.WindowSeconds != nil || req.Action != nil ||
		req.ActionParam != nil || req.Scope != nil || req.Enabled != nil
}

func rawOrNil(raw jsonx.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

func scanRiskRule(row rowScanner) (riskRuleView, error) {
	var (
		v         riskRuleView
		actionRaw []byte
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(&v.ID, &v.RuleName, &v.Metric, &v.Operator, &v.Threshold,
		&v.WindowSeconds, &v.Action, &actionRaw, &v.Scope, &v.Enabled,
		&v.CreatedBy, &createdAt, &updatedAt); err != nil {
		return riskRuleView{}, err
	}
	if len(actionRaw) > 0 {
		v.ActionParam = actionRaw
	}
	v.CreatedAt = createdAt.UTC()
	v.UpdatedAt = updatedAt.UTC()
	return v, nil
}

func loadRiskRuleTx(ctx context.Context, q priceDB, id int64, forUpdate bool) (riskRuleView, *apierr.APIError) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	v, err := scanRiskRule(q.QueryRowContext(ctx, `
		SELECT `+riskRuleSelectColumns+` FROM t_risk_rule r WHERE r.id = ?`+suffix, id))
	if errors.Is(err, sql.ErrNoRows) {
		return riskRuleView{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		return riskRuleView{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return v, nil
}

func riskRuleAuditView(v riskRuleView) map[string]any {
	return map[string]any{
		"rule_name":      v.RuleName,
		"metric":         v.Metric,
		"operator":       v.Operator,
		"threshold":      v.Threshold,
		"window_seconds": v.WindowSeconds,
		"action":         v.Action,
		"action_param":   rawOrNil(v.ActionParam),
		"scope":          v.Scope,
		"enabled":        v.Enabled,
	}
}
