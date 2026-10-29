package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/apikey"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
)

// Group codes defined by the PRD. Any other code is data, not a product
// constant, but the ratio rules below still key off "internal".
const groupCodeInternal = "internal"

type groupView struct {
	ID               int64     `json:"id"`
	Code             string    `json:"code"`
	Name             string    `json:"name"`
	RatioBP          int64     `json:"ratio_bp"`
	ModelScopeMode   string    `json:"model_scope_mode"`
	ChannelScopeMode string    `json:"channel_scope_mode"`
	SelfService      bool      `json:"self_service"`
	MinMarginBP      *int64    `json:"min_margin_bp,omitempty"`
	Remark           string    `json:"remark"`
	Enabled          bool      `json:"enabled"`
	ModelIDs         []int64   `json:"model_ids"`
	ChannelIDs       []int64   `json:"channel_ids"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type groupListResponse struct {
	Items []groupView `json:"items"`
	Total int64       `json:"total"`
}

type groupKeySync struct {
	Keys int    `json:"keys"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

type groupDetailResponse struct {
	Group   groupView    `json:"group"`
	KeySync groupKeySync `json:"key_sync"`
	// Warnings is additive (T31): models in scope whose floor could not be
	// checked because they have no cost price. Empty means every in-scope
	// model was floor-checked.
	Warnings []string `json:"warnings,omitempty"`
}

type groupPatchRequest struct {
	Name             *string           `json:"name"`
	Ratio            *jsonx.RawMessage `json:"ratio"`
	RatioBP          *int64            `json:"ratio_bp"`
	ModelScopeMode   *string           `json:"model_scope_mode"`
	ChannelScopeMode *string           `json:"channel_scope_mode"`
	SelfService      *bool             `json:"self_service"`
	MinMarginBP      *int64            `json:"min_margin_bp"`
	Remark           *string           `json:"remark"`
	Enabled          *bool             `json:"enabled"`
	ModelIDs         *[]int64          `json:"model_ids"`
	ChannelIDs       *[]int64          `json:"channel_ids"`
	// Confirm and Reason are the T31 second confirmation for internal saves
	// and for ratio changes that skip the floor (models without cost).
	Confirm *bool   `json:"confirm"`
	Reason  *string `json:"reason"`
}

const groupSelectColumns = `
	g.id, g.code, g.name, g.ratio_bp, g.model_scope_mode, g.channel_scope_mode,
	g.self_service, g.min_margin_bp, g.remark, g.enabled, g.created_at, g.updated_at`

// ListGroups implements GET /api/admin/groups.
func (s *Service) ListGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT `+groupSelectColumns+`
		  FROM t_price_group g ORDER BY g.ratio_bp DESC, g.id`)
	if err != nil {
		s.logError(r.Context(), "ops_group_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]groupView, 0, 4)
	ids := make([]int64, 0, 4)
	for rows.Next() {
		v, err := scanGroup(rows)
		if err != nil {
			s.logError(r.Context(), "ops_group_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, v)
		ids = append(ids, v.ID)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_group_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	scopes, err := loadGroupScopes(r.Context(), s.DB, ids)
	if err != nil {
		s.logError(r.Context(), "ops_group_scopes_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	for i := range items {
		if sc, ok := scopes[items[i].ID]; ok {
			items[i].ModelIDs = sc.models
			items[i].ChannelIDs = sc.channels
		}
	}
	_ = mw.WriteJSON(w, http.StatusOK, groupListResponse{Items: items, Total: int64(len(items))})
}

// UpdateGroup implements PATCH /api/admin/groups/{id}.
func (s *Service) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req groupPatchRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if !hasGroupPatch(&req) {
		mw.WriteError(w, r, mw.InvalidField("body"))
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_group_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	before, apiErr := s.loadGroupTx(ctx, tx, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if sc, err := loadGroupScopes(ctx, tx, []int64{id}); err == nil {
		if got, ok := sc[id]; ok {
			before.ModelIDs = got.models
			before.ChannelIDs = got.channels
		}
	} else {
		s.logError(ctx, "ops_group_before_scopes_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	sets := make([]string, 0, 10)
	args := make([]any, 0, 12)
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len([]rune(name)) > 64 {
			mw.WriteError(w, r, mw.InvalidField("name"))
			return
		}
		add("name", name)
	}

	nextRatio := before.RatioBP
	ratioChanged := false
	if req.Ratio != nil || req.RatioBP != nil {
		bp, apiErr := mergeGroupRatio(before, &req)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		nextRatio = bp
		ratioChanged = bp != before.RatioBP
		if !ratioChanged {
			// Keep the column out of the SET list; avoids a useless write.
		} else {
			add("ratio_bp", bp)
		}
	}

	modelScopeMode := before.ModelScopeMode
	if req.ModelScopeMode != nil {
		mode, apiErr := groupScopeMode(*req.ModelScopeMode, "model_scope_mode")
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		modelScopeMode = mode
		add("model_scope_mode", mode)
	}
	channelScopeMode := before.ChannelScopeMode
	if req.ChannelScopeMode != nil {
		mode, apiErr := groupScopeMode(*req.ChannelScopeMode, "channel_scope_mode")
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		channelScopeMode = mode
		add("channel_scope_mode", mode)
	}
	if req.SelfService != nil {
		add("self_service", *req.SelfService)
	}
	if req.MinMarginBP != nil {
		if *req.MinMarginBP < 0 || *req.MinMarginBP > 10000 {
			mw.WriteError(w, r, mw.InvalidField("min_margin_bp"))
			return
		}
		add("min_margin_bp", *req.MinMarginBP)
	}
	if req.Remark != nil {
		remark := strings.TrimSpace(*req.Remark)
		if len([]rune(remark)) > 255 {
			mw.WriteError(w, r, mw.InvalidField("remark"))
			return
		}
		add("remark", remark)
	}
	if req.Enabled != nil {
		add("enabled", *req.Enabled)
	}

	// internal's 0 ratio is the only zero allowed, and its meaning ("not
	// billed", not "free discount") must stay written down.
	auditReason := ""
	if before.Code == groupCodeInternal {
		if nextRatio != 0 {
			mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
				fmt.Sprintf("internal 分组的倍率只能是 0（语义为不计费），不能设为 %d", nextRatio)))
			return
		}
		remark := before.Remark
		if req.Remark != nil {
			remark = strings.TrimSpace(*req.Remark)
		}
		if remark == "" {
			mw.WriteError(w, r, mw.InvalidField("remark"))
			return
		}
		// T31 D-28 ②: every internal save needs an explicit confirmation and
		// a written reason; the reason goes into t_admin_audit_log.reason.
		reason := ""
		if req.Reason != nil {
			reason = strings.TrimSpace(*req.Reason)
		}
		if req.Confirm == nil || !*req.Confirm {
			mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
				"internal 分组保存须二次确认：请传 confirm=true 并填写 reason"))
			return
		}
		if reason == "" {
			mw.WriteError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
				"internal 分组保存必须填写 reason（操作理由），审计将记录该理由"))
			return
		}
		auditReason = reason
		// T31 D-28 ④: internal must not include the current main channel.
		if apiErr := s.checkInternalGroupChannels(r.Context(), tx, &req, before, channelScopeMode); apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
	}

	// T31 D-28 2/4: a non-internal ratio change is floor-checked against every
	// in-scope model. Models without a cost price are warned about instead of
	// being silently skipped, and the operator must confirm before saving.
	var warnings []string
	if before.Code != groupCodeInternal && ratioChanged && nextRatio > 0 {
		var apiErr *apierr.APIError
		warnings, apiErr = s.checkGroupRatioFloor(r.Context(), tx, before, nextRatio,
			modelScopeMode, effectiveModelIDs(before, &req), now)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		if len(warnings) > 0 && (req.Confirm == nil || !*req.Confirm) {
			writeGroupWarningError(w, r, apierr.Newf(apierr.CodeInvalidRequestBody,
				"分组范围内存在无成本价的模型，毛利地板无法全部校验；确认继续请传 confirm=true"),
				warnings)
			return
		}
	}

	if len(sets) > 0 {
		add("updated_at", now)
		if _, err := tx.ExecContext(ctx,
			`UPDATE t_price_group SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
			append(args, id)...); err != nil {
			s.logError(ctx, "ops_group_update_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
	}

	if req.ModelIDs != nil || req.ChannelIDs != nil {
		if apiErr := replaceGroupScopes(ctx, tx, id, req.ModelIDs, req.ChannelIDs, now); apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
	}

	after, apiErr := s.loadGroupTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	scopes, err := loadGroupScopes(ctx, tx, []int64{id})
	if err != nil {
		s.logError(ctx, "ops_group_scopes_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if sc, ok := scopes[id]; ok {
		after.ModelIDs = sc.models
		after.ChannelIDs = sc.channels
	}
	if err := s.insertAudit(ctx, tx, r, "GROUP", "UPDATE", "t_price_group", id,
		groupAuditView(before), groupAuditView(after), auditReason); err != nil {
		s.logError(ctx, "ops_group_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_group_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	sync := groupKeySync{OK: true}
	if ratioChanged {
		sync = s.dispatchGroupRatioSync(ctx, after.Code, after.RatioBP)
	}
	_ = mw.WriteJSON(w, http.StatusOK, groupDetailResponse{
		Group: after, KeySync: sync, Warnings: warnings})
}

// effectiveModelIDs is the SELECTED scope the request will leave behind, used
// for the floor check before any write happens.
func effectiveModelIDs(cur groupView, req *groupPatchRequest) []int64 {
	if req.ModelIDs != nil {
		return *req.ModelIDs
	}
	return cur.ModelIDs
}

// checkInternalGroupChannels enforces T31 D-28 ④: internal may not route
// through the current main channel (highest weight among enabled channels).
// Only an explicit channel scope can violate the rule; the seeded ALL mode
// with no scope rows is left alone so existing internal saves keep working.
func (s *Service) checkInternalGroupChannels(ctx context.Context, q priceDB,
	req *groupPatchRequest, before groupView, mode string) *apierr.APIError {
	if mode != scopeSelected && req.ChannelIDs == nil {
		return nil
	}
	ids := before.ChannelIDs
	if req.ChannelIDs != nil {
		ids = *req.ChannelIDs
	}
	if len(ids) == 0 {
		return nil
	}
	var mainID int64
	var mainName string
	err := q.QueryRowContext(ctx, `
		SELECT id, name FROM t_channel
		 WHERE enabled = 1 AND deleted_at IS NULL
		 ORDER BY weight DESC, id LIMIT 1`).Scan(&mainID, &mainName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	for _, id := range ids {
		if id == mainID {
			return apierr.Newf(apierr.CodeInvalidRequestBody, fmt.Sprintf(
				"internal 分组的可用渠道不能包含当前权重最高的启用渠道「%s」(id=%d)，请改用非主力渠道",
				mainName, mainID))
		}
	}
	return nil
}

type groupFloorModel struct {
	name      string
	modelType string
	inPrice   int64
	outPrice  int64
	perCall   int64
	costIn    sql.NullInt64
	costOut   sql.NullInt64
	costCall  sql.NullInt64
}

func loadGroupFloorModels(ctx context.Context, q priceDB, mode string, ids []int64, now time.Time) ([]groupFloorModel, error) {
	where := "m.deleted_at IS NULL"
	args := []any{now, now}
	if mode != scopeSelected {
		where += " AND m.listing_state = 'LISTED'"
	} else {
		if len(ids) == 0 {
			return nil, nil
		}
		where += " AND m.id IN (" + placeholders(len(ids)) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT m.model_name, m.model_type,
		       COALESCE(p.in_price_micro_per_k, 0),
		       COALESCE(p.out_price_micro_per_k, 0),
		       COALESCE(p.per_call_price_micro, 0),
		       p.cost_in_micro_per_k, p.cost_out_micro_per_k, p.cost_per_call_micro
		  FROM t_model m
		  LEFT JOIN t_price_rule p ON p.id = (
		       SELECT p2.id FROM t_price_rule p2
		        WHERE p2.scope_level = 'MODEL' AND p2.scope_id = m.id AND p2.enabled = 1
		          AND p2.effective_from <= ? AND (p2.effective_to IS NULL OR p2.effective_to > ?)
		        ORDER BY p2.version DESC, p2.id DESC LIMIT 1)
		 WHERE `+where+`
		 ORDER BY m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []groupFloorModel
	for rows.Next() {
		var m groupFloorModel
		if err := rows.Scan(&m.name, &m.modelType, &m.inPrice, &m.outPrice, &m.perCall,
			&m.costIn, &m.costOut, &m.costCall); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type groupFloorFailure struct {
	field    string
	unit     string
	actual   int64
	required int64
}

// checkGroupRatioFloor applies the price-side floor to every in-scope model at
// the new ratio. It never returns a refusal for a missing cost: those models
// come back as warnings and the caller demands confirm=true before writing.
func (s *Service) checkGroupRatioFloor(ctx context.Context, q priceDB, grp groupView,
	ratio int64, mode string, ids []int64, now time.Time) ([]string, *apierr.APIError) {
	models, err := loadGroupFloorModels(ctx, q, mode, ids, now)
	if err != nil {
		s.logError(ctx, "ops_group_floor_models_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	var (
		warnings  []string
		failNames []string
		first     *groupFloorFailure
	)
	for _, m := range models {
		dims := marginDimensions(m.modelType, m.inPrice, m.outPrice, m.perCall,
			nullableInt64Ptr(m.costIn), nullableInt64Ptr(m.costOut), nullableInt64Ptr(m.costCall))
		hasCost := false
		for _, d := range dims {
			if d.cost == nil || *d.cost == 0 {
				continue
			}
			hasCost = true
			actual, required, ok := marginFloor(d.price, ratio, *d.cost)
			if ok {
				continue
			}
			if first == nil {
				first = &groupFloorFailure{field: d.field, unit: d.unit, actual: actual, required: required}
			}
			failNames = append(failNames, m.name)
			break
		}
		if !hasCost {
			warnings = append(warnings, fmt.Sprintf("模型 %s 缺成本价，地板未校验", m.name))
		}
	}
	if first != nil {
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody, fmt.Sprintf(
			"分组 %s（倍率 %d）下 %s 实际值 %d %s 低于地板值 %d（%d < %d），差额 %d；涉及模型：%s",
			grp.Code, ratio, first.field, first.actual, first.unit, first.required,
			first.actual, first.required, first.required-first.actual,
			modelNameList(failNames, 5)))
	}
	return warnings, nil
}

// modelNameList names at most max models; the rest are counted, not silently
// dropped, so the operator can size the blast radius.
func modelNameList(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, "、")
	}
	return fmt.Sprintf("%s 等 %d 个", strings.Join(names[:max], "、"), len(names))
}

// writeGroupWarningError writes the standard error envelope plus a top-level
// warnings[] array. mw.WriteError cannot carry extra fields, but T31 G5
// requires the refusal itself to name the models that were not checked.
func writeGroupWarningError(w http.ResponseWriter, r *http.Request,
	apiErr *apierr.APIError, warnings []string) {
	if rid := obslog.MetaFrom(r.Context()).RequestID; rid != "" {
		apiErr = apiErr.WithRequestID(rid)
	}
	// apierr.Write is the single error path and always sets X-Request-Id. This
	// helper hand-rolls the envelope to add warnings[], so it must reproduce
	// that header; EnsureRequestID also fills the body's request_id.
	rid := apiErr.EnsureRequestID()
	raw, err := jsonx.Marshal(apiErr)
	if err != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var env map[string]any
	if err := jsonx.Unmarshal(raw, &env); err != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	env["warnings"] = warnings
	w.Header().Set("X-Request-Id", rid)
	_ = mw.WriteJSON(w, apiErr.HTTPStatus, env)
}

// dispatchGroupRatioSync runs the K13 fan-out after the response so a group
// with many keys does not block the operator request. The trade-off is the
// one T15 allows: K13 may carry the old gp for a moment; the one-minute
// Refresher repairs it. The request context is kept (WithoutCancel) but
// bounded, because the response has already been written.
func (s *Service) dispatchGroupRatioSync(ctx context.Context, code string, ratioBP int64) groupKeySync {
	if s.KeyAuth == nil {
		return groupKeySync{OK: false, Note: "K13 writer not configured"}
	}
	go func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		result := s.writeThroughGroupRatio(cctx, code, ratioBP)
		if !result.OK {
			s.logWarn(cctx, "ops_group_k13_async_failed: "+result.Note)
		}
	}()
	return groupKeySync{OK: true, Note: "异步写穿；失败由 Refresher 修复"}
}

// writeThroughGroupRatio updates K13's gp for every live key in the group.
// K13 is not indexable by group, so the affected hashes come from t_api_key.
// A failure is logged but not rolled back: the database is authoritative and
// the one-minute Refresher repairs K13, which is exactly the "brief stale
// value" trade-off T15 allows.
func (s *Service) writeThroughGroupRatio(ctx context.Context, code string, ratioBP int64) groupKeySync {
	if s.KeyAuth == nil {
		return groupKeySync{OK: false, Note: "K13 writer not configured"}
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT key_hash FROM t_api_key WHERE group_code = ? AND deleted_at IS NULL`, code)
	if err != nil {
		s.logWarn(ctx, "ops_group_key_load_failed: "+err.Error())
		return groupKeySync{OK: false, Note: "load keys failed"}
	}
	defer rows.Close()

	hashes := make([]string, 0, 16)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			s.logWarn(ctx, "ops_group_key_scan_failed: "+err.Error())
			return groupKeySync{OK: false, Note: "scan keys failed"}
		}
		hash16, err := apikey.HashPrefix16(hash)
		if err != nil {
			s.logWarn(ctx, "ops_group_key_hash_failed: "+err.Error())
			continue
		}
		hashes = append(hashes, hash16)
	}
	if err := rows.Err(); err != nil {
		s.logWarn(ctx, "ops_group_key_rows_failed: "+err.Error())
		return groupKeySync{OK: false, Note: "iterate keys failed"}
	}
	if err := s.KeyAuth.BulkUpdateGroupRatio(ctx, hashes, ratioBP); err != nil {
		s.logWarn(ctx, "ops_group_k13_write_failed: "+err.Error())
		return groupKeySync{Keys: len(hashes), OK: false, Note: "K13 write failed; Refresher will repair"}
	}
	return groupKeySync{Keys: len(hashes), OK: true}
}

// ---------------------------------------------------------------------------
// ratio parsing
// ---------------------------------------------------------------------------

// mergeGroupRatio accepts either the human decimal ("0.85" → 8500) or the
// stored basis points. Decimal parsing is integer-only: no float64 touches a
// price dimension (T15 hard constraint 9).
func mergeGroupRatio(cur groupView, req *groupPatchRequest) (int64, *apierr.APIError) {
	var (
		bp      int64
		hasBP   bool
		fromDec int64
		hasDec  bool
	)
	if req.RatioBP != nil {
		bp = *req.RatioBP
		hasBP = true
	}
	if req.Ratio != nil {
		v, apiErr := decimalRatioBP(*req.Ratio)
		if apiErr != nil {
			return 0, apiErr
		}
		fromDec = v
		hasDec = true
	}
	if hasBP && hasDec && bp != fromDec {
		return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("ratio 与 ratio_bp 不一致：%d ≠ %d", fromDec, bp))
	}
	if !hasBP && !hasDec {
		return cur.RatioBP, nil
	}
	if !hasBP {
		bp = fromDec
	}

	switch {
	case cur.Code == groupCodeInternal:
		if bp != 0 {
			return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
				fmt.Sprintf("internal 分组的倍率只能是 0（语义为不计费），当前请求 %d", bp))
		}
	case bp <= 0:
		return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("分组倍率必须大于 0（internal 的 0 是唯一例外），当前 %d", bp))
	case bp > 10000:
		return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("分组倍率不能大于 1.0（10000 万分比），当前 %d", bp))
	}
	return bp, nil
}

// decimalRatioBP converts a JSON number or string such as 0.85 into basis
// points without a float. More than four fractional digits is rejected rather
// than rounded: a silent rounding of a price multiplier is a billing bug.
func decimalRatioBP(raw jsonx.RawMessage) (int64, *apierr.APIError) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := jsonx.Unmarshal(raw, &s); err != nil {
			return 0, mw.InvalidField("ratio")
		}
		text = strings.TrimSpace(s)
	}
	if text == "" || strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
		return 0, mw.InvalidField("ratio")
	}
	parts := strings.SplitN(text, ".", 2)
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if intPart == "" && fracPart == "" {
		return 0, mw.InvalidField("ratio")
	}
	if intPart == "" {
		intPart = "0"
	}
	for _, s := range []string{intPart, fracPart} {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return 0, mw.InvalidField("ratio")
			}
		}
	}
	fracPart = strings.TrimRight(fracPart, "0")
	if len(fracPart) > 4 {
		return 0, apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("ratio 最多 4 位小数（万分比精度），当前 %s", text))
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || whole > 1_000_000 {
		return 0, mw.InvalidField("ratio")
	}
	for len(fracPart) < 4 {
		fracPart += "0"
	}
	frac := int64(0)
	if fracPart != "" {
		frac, err = strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return 0, mw.InvalidField("ratio")
		}
	}
	return whole*groupRatioScale + frac, nil
}

func groupScopeMode(raw, field string) (string, *apierr.APIError) {
	v := strings.ToUpper(strings.TrimSpace(raw))
	if v != scopeAll && v != scopeSelected {
		return "", mw.InvalidField(field)
	}
	return v, nil
}

func hasGroupPatch(req *groupPatchRequest) bool {
	return req.Name != nil || req.Ratio != nil || req.RatioBP != nil ||
		req.ModelScopeMode != nil || req.ChannelScopeMode != nil ||
		req.SelfService != nil || req.MinMarginBP != nil || req.Remark != nil ||
		req.Enabled != nil || req.ModelIDs != nil || req.ChannelIDs != nil
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

func scanGroup(row rowScanner) (groupView, error) {
	var (
		v         groupView
		minMargin sql.NullInt64
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(&v.ID, &v.Code, &v.Name, &v.RatioBP, &v.ModelScopeMode,
		&v.ChannelScopeMode, &v.SelfService, &minMargin, &v.Remark, &v.Enabled,
		&createdAt, &updatedAt); err != nil {
		return groupView{}, err
	}
	v.MinMarginBP = nullableInt64Ptr(minMargin)
	v.ModelIDs = []int64{}
	v.ChannelIDs = []int64{}
	v.CreatedAt = createdAt.UTC()
	v.UpdatedAt = updatedAt.UTC()
	return v, nil
}

func (s *Service) loadGroupTx(ctx context.Context, q priceDB, id int64, forUpdate bool) (groupView, *apierr.APIError) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	v, err := scanGroup(q.QueryRowContext(ctx,
		`SELECT `+groupSelectColumns+` FROM t_price_group g WHERE g.id = ?`+suffix, id))
	if errors.Is(err, sql.ErrNoRows) {
		return groupView{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		return groupView{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return v, nil
}

type groupScopes struct {
	models   []int64
	channels []int64
}

func loadGroupScopes(ctx context.Context, q priceDB, ids []int64) (map[int64]groupScopes, error) {
	out := make(map[int64]groupScopes, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `
		SELECT group_id, scope_type, scope_id FROM t_price_group_scope
		 WHERE group_id IN (`+placeholders(len(ids))+`)
		 ORDER BY group_id, scope_type, scope_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			groupID int64
			kind    string
			scopeID int64
		)
		if err := rows.Scan(&groupID, &kind, &scopeID); err != nil {
			return nil, err
		}
		sc := out[groupID]
		switch kind {
		case "MODEL":
			sc.models = append(sc.models, scopeID)
		case "CHANNEL":
			sc.channels = append(sc.channels, scopeID)
		}
		out[groupID] = sc
	}
	return out, rows.Err()
}

func replaceGroupScopes(ctx context.Context, tx *sql.Tx, groupID int64, modelIDs, channelIDs *[]int64, now time.Time) *apierr.APIError {
	if modelIDs != nil {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM t_price_group_scope WHERE group_id = ? AND scope_type = 'MODEL'`, groupID); err != nil {
			return apierr.New(apierr.CodeInternalError).WithCause(err)
		}
		ids, apiErr := normalizeScopeIDs(*modelIDs, "model_ids")
		if apiErr != nil {
			return apiErr
		}
		if apiErr := insertGroupScope(ctx, tx, groupID, "MODEL", ids, now); apiErr != nil {
			return apiErr
		}
	}
	if channelIDs != nil {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM t_price_group_scope WHERE group_id = ? AND scope_type = 'CHANNEL'`, groupID); err != nil {
			return apierr.New(apierr.CodeInternalError).WithCause(err)
		}
		ids, apiErr := normalizeScopeIDs(*channelIDs, "channel_ids")
		if apiErr != nil {
			return apiErr
		}
		if apiErr := insertGroupScope(ctx, tx, groupID, "CHANNEL", ids, now); apiErr != nil {
			return apiErr
		}
	}
	return nil
}

func normalizeScopeIDs(ids []int64, field string) ([]int64, *apierr.APIError) {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, mw.InvalidField(field)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) > 1000 {
		return nil, mw.InvalidField(field)
	}
	return out, nil
}

func insertGroupScope(ctx context.Context, tx *sql.Tx, groupID int64, kind string, ids []int64, now time.Time) *apierr.APIError {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO t_price_group_scope (group_id, scope_type, scope_id, created_at)
			VALUES (?,?,?,?)`, groupID, kind, id, now); err != nil {
			return apierr.New(apierr.CodeInternalError).WithCause(err)
		}
	}
	return nil
}

func groupAuditView(v groupView) map[string]any {
	return map[string]any{
		"code":               v.Code,
		"name":               v.Name,
		"ratio_bp":           v.RatioBP,
		"model_scope_mode":   v.ModelScopeMode,
		"channel_scope_mode": v.ChannelScopeMode,
		"self_service":       v.SelfService,
		"min_margin_bp":      v.MinMarginBP,
		"remark":             v.Remark,
		"enabled":            v.Enabled,
		"model_ids":          v.ModelIDs,
		"channel_ids":        v.ChannelIDs,
	}
}
