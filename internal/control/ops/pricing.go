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
	"yunshu.dev/core/internal/control/mw"
)

// priceNoticePeriod is the B-66 public-notice window for a price increase.
// A decrease has no window (PRD F16-4): it may take effect immediately.
const priceNoticePeriod = 7 * 24 * time.Hour

// floorDenominator encodes 4/3 with no division at comparison time:
//
//	3*P*r/10000 >= 4*C/3  ⇔  3*P*r >= 40000*C
//
// The required price shown in a refusal is ceil(4*C/3), i.e.
// (40000*C + 29999) / 30000. Both are integer operations; a float here would
// lose a credit on large costs and is banned by T15 hard constraint 4.
const (
	floorCrossFactor = 40000
	floorScale       = 30000
	groupRatioScale  = 10000
)

// priceRuleView is the operator representation of one t_price_rule row.
type priceRuleView struct {
	ID         int64  `json:"id"`
	ScopeLevel string `json:"scope_level"`
	ModelID    int64  `json:"model_id"`
	ModelName  string `json:"model_name"`
	ModelType  string `json:"model_type"`

	InPriceMicroPerK  int64 `json:"in_price_micro_per_k"`
	OutPriceMicroPerK int64 `json:"out_price_micro_per_k"`
	PerCallPriceMicro int64 `json:"per_call_price_micro"`

	CacheHitRatioBP   *int64 `json:"cache_hit_ratio_bp,omitempty"`
	CacheWriteRatioBP *int64 `json:"cache_write_ratio_bp,omitempty"`

	LongCtxThresholdTokens   *int64 `json:"long_ctx_threshold_tokens,omitempty"`
	LongCtxInPriceMicroPerK  *int64 `json:"long_ctx_in_price_micro_per_k,omitempty"`
	LongCtxOutPriceMicroPerK *int64 `json:"long_ctx_out_price_micro_per_k,omitempty"`

	CostInMicroPerK  *int64 `json:"cost_in_micro_per_k,omitempty"`
	CostOutMicroPerK *int64 `json:"cost_out_micro_per_k,omitempty"`
	CostPerCallMicro *int64 `json:"cost_per_call_micro,omitempty"`

	CurrencyCost  string     `json:"currency_cost"`
	Version       int64      `json:"version"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
}

type priceListResponse struct {
	Items    []priceRuleView `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}

type priceDetailResponse struct {
	Price priceRuleView `json:"price"`
}

// priceWriteRequest is shared by POST and PATCH. Every field is a pointer so
// PATCH can tell "leave it alone" from "set it to zero".
type priceWriteRequest struct {
	ModelID *int64 `json:"model_id"`

	InPriceMicroPerK  *int64 `json:"in_price_micro_per_k"`
	OutPriceMicroPerK *int64 `json:"out_price_micro_per_k"`
	PerCallPriceMicro *int64 `json:"per_call_price_micro"`

	CacheHitRatioBP   *int64 `json:"cache_hit_ratio_bp"`
	CacheWriteRatioBP *int64 `json:"cache_write_ratio_bp"`

	LongCtxThresholdTokens   *int64 `json:"long_ctx_threshold_tokens"`
	LongCtxInPriceMicroPerK  *int64 `json:"long_ctx_in_price_micro_per_k"`
	LongCtxOutPriceMicroPerK *int64 `json:"long_ctx_out_price_micro_per_k"`

	CostInMicroPerK  *int64 `json:"cost_in_micro_per_k"`
	CostOutMicroPerK *int64 `json:"cost_out_micro_per_k"`
	CostPerCallMicro *int64 `json:"cost_per_call_micro"`

	CurrencyCost  *string    `json:"currency_cost"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	Enabled       *bool      `json:"enabled"`

	// ConfirmNoCost is the second confirmation required when a sell price has
	// no matching upstream cost. A missing cost must never silently list a
	// model below the floor (T15 hard constraint from F16-5).
	ConfirmNoCost bool `json:"confirm_no_cost"`
}

// priceInput is the fully merged, validated row.
type priceInput struct {
	modelID   int64
	modelType string

	inPrice, outPrice, perCall int64
	cacheHit, cacheWrite       *int64
	longThreshold              *int64
	longIn, longOut            *int64
	costIn, costOut, costCall  *int64

	currency      string
	effectiveFrom time.Time
	effectiveTo   *time.Time
	enabled       bool

	// confirmNoCost records the operator's explicit acknowledgement that a
	// sell price has no upstream cost. It is echoed into the audit row.
	confirmNoCost bool
}

// priceRow is the storage shape, including everything needed to compare two
// sources for the A12 conflict check.
type priceRow struct {
	priceRuleView
}

// signatureFull is the complete set of price columns. Two overlapping rows
// with the same signature are duplicates the loader can ignore; different
// signatures are the A12 conflict.
func (p priceRow) signatureFull() string {
	return fmt.Sprintf("%d|%d|%d|%s|%s|%s|%s|%s|%s|%s|%s",
		p.InPriceMicroPerK, p.OutPriceMicroPerK, p.PerCallPriceMicro,
		intPtrString(p.CacheHitRatioBP), intPtrString(p.CacheWriteRatioBP),
		intPtrString(p.LongCtxThresholdTokens), intPtrString(p.LongCtxInPriceMicroPerK),
		intPtrString(p.LongCtxOutPriceMicroPerK), intPtrString(p.CostInMicroPerK),
		intPtrString(p.CostOutMicroPerK), intPtrString(p.CostPerCallMicro))
}

func intPtrString(v *int64) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *v)
}

const priceSelectColumns = `
	p.id, p.scope_level, p.scope_id, m.model_name, p.model_type,
	p.in_price_micro_per_k, p.out_price_micro_per_k, p.per_call_price_micro,
	p.cache_hit_ratio_bp, p.cache_write_ratio_bp,
	p.long_ctx_threshold_tokens, p.long_ctx_in_price_micro_per_k, p.long_ctx_out_price_micro_per_k,
	p.cost_in_micro_per_k, p.cost_out_micro_per_k, p.cost_per_call_micro,
	p.currency_cost, p.version, p.effective_from, p.effective_to, p.enabled, p.created_at`

// ListPrices implements GET /api/admin/prices.
func (s *Service) ListPrices(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r, "created_at", "effective_from", "version", "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := r.URL.Query()
	where := []string{"p.scope_level = 'MODEL'", "m.deleted_at IS NULL"}
	args := make([]any, 0, 4)

	if raw := strings.TrimSpace(q.Get("model_id")); raw != "" {
		modelID, err := parseInt64Query(raw)
		if err != nil || modelID <= 0 {
			mw.WriteError(w, r, mw.InvalidQueryField("model_id"))
			return
		}
		where = append(where, "p.scope_id = ?")
		args = append(args, modelID)
	}
	if raw := strings.TrimSpace(q.Get("model_type")); raw != "" {
		mt := strings.ToUpper(raw)
		if mt != "TEXT" && mt != "IMAGE" && mt != "EMBEDDING" {
			mw.WriteError(w, r, mw.InvalidQueryField("model_type"))
			return
		}
		where = append(where, "p.model_type = ?")
		args = append(args, mt)
	}
	if raw := strings.TrimSpace(q.Get("enabled")); raw != "" {
		if raw != "0" && raw != "1" {
			mw.WriteError(w, r, mw.InvalidQueryField("enabled"))
			return
		}
		where = append(where, "p.enabled = ?")
		args = append(args, raw)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM t_price_rule p JOIN t_model m ON m.id = p.scope_id WHERE `+whereSQL,
		args...).Scan(&total); err != nil {
		s.logError(r.Context(), "ops_price_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	orderBy := "p.created_at"
	switch page.Sort {
	case "effective_from":
		orderBy = "p.effective_from"
	case "version":
		orderBy = "p.version"
	case "id":
		orderBy = "p.id"
	}
	dir := orderDirection(page.Order)
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT `+priceSelectColumns+`
		  FROM t_price_rule p JOIN t_model m ON m.id = p.scope_id
		 WHERE `+whereSQL+`
		 ORDER BY `+orderBy+` `+dir+`, p.id `+dir+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(r.Context(), "ops_price_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]priceRuleView, 0, page.Size)
	for rows.Next() {
		row, err := scanPriceRule(rows)
		if err != nil {
			s.logError(r.Context(), "ops_price_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, row.priceRuleView)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_price_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, priceListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// CreatePrice implements POST /api/admin/prices.
//
// Order matters: the row is inserted before the conflict query so the 409 can
// name the new row id next to the row it collides with (A12 requires table
// name + row id for both sources). Everything runs in one transaction, so a
// conflict rolls the insert back.
func (s *Service) CreatePrice(w http.ResponseWriter, r *http.Request) {
	var req priceWriteRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.ModelID == nil || *req.ModelID <= 0 {
		mw.WriteError(w, r, mw.InvalidField("model_id"))
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_price_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	modelType, apiErr := s.modelTypeTx(ctx, tx, *req.ModelID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	in, apiErr := buildPriceInput(req, modelType, nil, now)
	in.confirmNoCost = req.ConfirmNoCost
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if apiErr := s.validatePriceChange(ctx, tx, in, now); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	version, err := nextPriceVersion(ctx, tx, in.modelID)
	if err != nil {
		s.logError(ctx, "ops_price_version_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_price_rule
			(scope_level, scope_id, model_type,
			 in_price_micro_per_k, out_price_micro_per_k, per_call_price_micro,
			 cache_hit_ratio_bp, cache_write_ratio_bp,
			 long_ctx_threshold_tokens, long_ctx_in_price_micro_per_k, long_ctx_out_price_micro_per_k,
			 cost_in_micro_per_k, cost_out_micro_per_k, cost_per_call_micro,
			 currency_cost, markup_factor_bp, version, effective_from, effective_to,
			 enabled, created_by, created_at)
		VALUES ('MODEL',?,?,?,?,?,?,?,?,?,?,?,?,?,?,10000,?,?,?,?,?,?)`,
		in.modelID, in.modelType,
		in.inPrice, in.outPrice, in.perCall,
		nullInt64(in.cacheHit), nullInt64(in.cacheWrite),
		nullInt64(in.longThreshold), nullInt64(in.longIn), nullInt64(in.longOut),
		nullInt64(in.costIn), nullInt64(in.costOut), nullInt64(in.costCall),
		in.currency, version, in.effectiveFrom, nullTimeValue(in.effectiveTo),
		in.enabled, mustActorID(r), now)
	if err != nil {
		s.logError(ctx, "ops_price_insert_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		s.logError(ctx, "ops_price_id_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	// A12: two overlapping enabled rows with different prices are a truth
	// source conflict, not a version to pick from.
	if apiErr := s.checkPriceConflicts(ctx, tx, in, id); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if apiErr := closePredecessors(ctx, tx, in, id); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	after, apiErr := loadPriceRuleTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	auditAfter := priceAuditView(after)
	if in.confirmNoCost {
		auditAfter["confirm_no_cost"] = true
	}
	if err := s.insertAudit(ctx, tx, r, "PRICE", "CREATE", "t_price_rule", id, nil,
		auditAfter, ""); err != nil {
		s.logError(ctx, "ops_price_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_price_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	if apiErr := s.refreshPrices(ctx); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusCreated, priceDetailResponse{Price: after.priceRuleView})
}

// UpdatePrice implements PATCH /api/admin/prices/{id}.
func (s *Service) UpdatePrice(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req priceWriteRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.ModelID != nil {
		// Moving a rule to another model would silently change two models'
		// prices. Delete/recreate instead.
		mw.WriteError(w, r, mw.InvalidField("model_id"))
		return
	}
	if !hasPricePatch(&req) {
		mw.WriteError(w, r, mw.InvalidField("body"))
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_price_patch_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	before, apiErr := loadPriceRuleTx(ctx, tx, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	in, apiErr := buildPriceInput(req, before.ModelType, &before, now)
	in.confirmNoCost = req.ConfirmNoCost
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if apiErr := s.validatePriceChange(ctx, tx, in, now); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_price_rule
		   SET in_price_micro_per_k = ?, out_price_micro_per_k = ?, per_call_price_micro = ?,
		       cache_hit_ratio_bp = ?, cache_write_ratio_bp = ?,
		       long_ctx_threshold_tokens = ?, long_ctx_in_price_micro_per_k = ?,
		       long_ctx_out_price_micro_per_k = ?,
		       cost_in_micro_per_k = ?, cost_out_micro_per_k = ?, cost_per_call_micro = ?,
		       currency_cost = ?, version = version + 1,
		       effective_from = ?, effective_to = ?, enabled = ?
		 WHERE id = ? AND scope_level = 'MODEL'`,
		in.inPrice, in.outPrice, in.perCall,
		nullInt64(in.cacheHit), nullInt64(in.cacheWrite),
		nullInt64(in.longThreshold), nullInt64(in.longIn), nullInt64(in.longOut),
		nullInt64(in.costIn), nullInt64(in.costOut), nullInt64(in.costCall),
		in.currency, in.effectiveFrom, nullTimeValue(in.effectiveTo), in.enabled, id); err != nil {
		s.logError(ctx, "ops_price_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if apiErr := s.checkPriceConflicts(ctx, tx, in, id); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	after, apiErr := loadPriceRuleTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	auditAfter := priceAuditView(after)
	if in.confirmNoCost {
		auditAfter["confirm_no_cost"] = true
	}
	if err := s.insertAudit(ctx, tx, r, "PRICE", "UPDATE", "t_price_rule", id,
		priceAuditView(before), auditAfter, ""); err != nil {
		s.logError(ctx, "ops_price_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_price_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true

	if apiErr := s.refreshPrices(ctx); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, priceDetailResponse{Price: after.priceRuleView})
}

// refreshPrices rebuilds K19. A committed price the gateway cannot see is
// reported as 503, exactly like a routing publish failure: the operator must
// know the change is not live yet.
func (s *Service) refreshPrices(ctx context.Context) *apierr.APIError {
	if s.PriceRefresh == nil {
		return nil
	}
	if err := s.PriceRefresh(ctx); err != nil {
		s.logError(ctx, "ops_price_refresh_failed: "+err.Error())
		return apierr.New(apierr.CodeConfigUnavailable).WithCause(err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func buildPriceInput(req priceWriteRequest, modelType string, cur *priceRow, now time.Time) (priceInput, *apierr.APIError) {
	var in priceInput
	in.modelType = modelType
	if cur != nil {
		in.modelID = cur.ModelID
		in.inPrice = cur.InPriceMicroPerK
		in.outPrice = cur.OutPriceMicroPerK
		in.perCall = cur.PerCallPriceMicro
		in.cacheHit = cur.CacheHitRatioBP
		in.cacheWrite = cur.CacheWriteRatioBP
		in.longThreshold = cur.LongCtxThresholdTokens
		in.longIn = cur.LongCtxInPriceMicroPerK
		in.longOut = cur.LongCtxOutPriceMicroPerK
		in.costIn = cur.CostInMicroPerK
		in.costOut = cur.CostOutMicroPerK
		in.costCall = cur.CostPerCallMicro
		in.currency = cur.CurrencyCost
		in.effectiveFrom = cur.EffectiveFrom
		in.effectiveTo = cur.EffectiveTo
		in.enabled = cur.Enabled
	} else {
		in.modelID = *req.ModelID
		in.currency = "CNY"
		in.enabled = true
		in.effectiveFrom = now
	}

	if req.InPriceMicroPerK != nil {
		in.inPrice = *req.InPriceMicroPerK
	}
	if req.OutPriceMicroPerK != nil {
		in.outPrice = *req.OutPriceMicroPerK
	}
	if req.PerCallPriceMicro != nil {
		in.perCall = *req.PerCallPriceMicro
	}
	if req.CacheHitRatioBP != nil {
		in.cacheHit = req.CacheHitRatioBP
	}
	if req.CacheWriteRatioBP != nil {
		in.cacheWrite = req.CacheWriteRatioBP
	}
	if req.LongCtxThresholdTokens != nil {
		in.longThreshold = req.LongCtxThresholdTokens
	}
	if req.LongCtxInPriceMicroPerK != nil {
		in.longIn = req.LongCtxInPriceMicroPerK
	}
	if req.LongCtxOutPriceMicroPerK != nil {
		in.longOut = req.LongCtxOutPriceMicroPerK
	}
	if req.CostInMicroPerK != nil {
		in.costIn = req.CostInMicroPerK
	}
	if req.CostOutMicroPerK != nil {
		in.costOut = req.CostOutMicroPerK
	}
	if req.CostPerCallMicro != nil {
		in.costCall = req.CostPerCallMicro
	}
	if req.CurrencyCost != nil {
		in.currency = strings.ToUpper(strings.TrimSpace(*req.CurrencyCost))
	}
	if req.EffectiveFrom != nil {
		in.effectiveFrom = req.EffectiveFrom.UTC()
	}
	if req.EffectiveTo != nil {
		in.effectiveTo = req.EffectiveTo
	}
	if req.Enabled != nil {
		in.enabled = *req.Enabled
	}

	// The notice window is judged against the server clock only. A client
	// cannot make a 3-day increase look like a 7-day one by sending its own
	// "now". A past effective_from on a create is clamped to now; a PATCH
	// without an explicit date keeps the row's existing schedule.
	if cur == nil || req.EffectiveFrom != nil {
		if in.effectiveFrom.Before(now) {
			in.effectiveFrom = now
		}
	}
	if in.effectiveTo != nil {
		to := in.effectiveTo.UTC()
		in.effectiveTo = &to
		if !to.After(in.effectiveFrom) {
			return in, mw.InvalidField("effective_to")
		}
	}

	if apiErr := validatePriceValues(in); apiErr != nil {
		return in, apiErr
	}
	return in, nil
}

func validatePriceValues(in priceInput) *apierr.APIError {
	switch in.modelType {
	case "TEXT":
		if in.inPrice <= 0 {
			return mw.InvalidField("in_price_micro_per_k")
		}
		if in.outPrice <= 0 {
			return mw.InvalidField("out_price_micro_per_k")
		}
		if in.perCall != 0 {
			return mw.InvalidField("per_call_price_micro")
		}
	case "IMAGE":
		if in.perCall <= 0 {
			return mw.InvalidField("per_call_price_micro")
		}
		if in.inPrice != 0 || in.outPrice != 0 {
			return mw.InvalidField("in_price_micro_per_k")
		}
	case "EMBEDDING":
		if in.inPrice <= 0 {
			return mw.InvalidField("in_price_micro_per_k")
		}
		if in.outPrice != 0 {
			return mw.InvalidField("out_price_micro_per_k")
		}
		if in.perCall != 0 {
			return mw.InvalidField("per_call_price_micro")
		}
	default:
		return mw.InvalidField("model_type")
	}

	for name, v := range map[string]*int64{
		"cache_hit_ratio_bp": in.cacheHit, "cache_write_ratio_bp": in.cacheWrite,
		"cost_in_micro_per_k": in.costIn, "cost_out_micro_per_k": in.costOut,
		"cost_per_call_micro": in.costCall,
	} {
		if v == nil {
			continue
		}
		if *v < 0 {
			return mw.InvalidField(name)
		}
	}
	if in.cacheHit != nil && *in.cacheHit > 100_000 {
		return mw.InvalidField("cache_hit_ratio_bp")
	}
	if in.cacheWrite != nil && *in.cacheWrite > 100_000 {
		return mw.InvalidField("cache_write_ratio_bp")
	}

	// The long-context chain is all-or-nothing: a threshold without both
	// prices, or a price without a threshold, would leave P09 with an
	// unreachable tier.
	if in.longThreshold == nil {
		if in.longIn != nil || in.longOut != nil {
			return mw.InvalidField("long_ctx_threshold_tokens")
		}
	} else {
		if *in.longThreshold <= 0 {
			return mw.InvalidField("long_ctx_threshold_tokens")
		}
		if in.longIn == nil || *in.longIn <= 0 {
			return mw.InvalidField("long_ctx_in_price_micro_per_k")
		}
		if in.longOut == nil || *in.longOut <= 0 {
			return mw.InvalidField("long_ctx_out_price_micro_per_k")
		}
	}

	if in.currency != "CNY" {
		return mw.InvalidField("currency_cost")
	}
	return nil
}

// isPriceIncrease compares only the customer-facing prices. Cost changes with
// unchanged sell prices are not a "涨价" and need no notice window.
func isPriceIncrease(in priceInput, cur priceRow) bool {
	if in.inPrice > cur.InPriceMicroPerK || in.outPrice > cur.OutPriceMicroPerK ||
		in.perCall > cur.PerCallPriceMicro {
		return true
	}
	if in.longIn != nil && cur.LongCtxInPriceMicroPerK != nil && *in.longIn > *cur.LongCtxInPriceMicroPerK {
		return true
	}
	if in.longOut != nil && cur.LongCtxOutPriceMicroPerK != nil && *in.longOut > *cur.LongCtxOutPriceMicroPerK {
		return true
	}
	return false
}

func priceRuleFromInput(in priceInput) priceRow {
	return priceRow{priceRuleView: priceRuleView{
		ModelID:                  in.modelID,
		ModelType:                in.modelType,
		InPriceMicroPerK:         in.inPrice,
		OutPriceMicroPerK:        in.outPrice,
		PerCallPriceMicro:        in.perCall,
		CacheHitRatioBP:          in.cacheHit,
		CacheWriteRatioBP:        in.cacheWrite,
		LongCtxThresholdTokens:   in.longThreshold,
		LongCtxInPriceMicroPerK:  in.longIn,
		LongCtxOutPriceMicroPerK: in.longOut,
		CostInMicroPerK:          in.costIn,
		CostOutMicroPerK:         in.costOut,
		CostPerCallMicro:         in.costCall,
	}}
}

// validatePriceChange runs the three write-time gates in the order that gives
// the operator the most specific refusal: floor (value), notice (schedule),
// conflicts (data integrity).
func (s *Service) validatePriceChange(ctx context.Context, tx *sql.Tx, in priceInput, now time.Time) *apierr.APIError {
	if apiErr := s.checkMarginFloor(ctx, tx, in); apiErr != nil {
		return apiErr
	}
	return s.checkNoticeAgainstActive(ctx, tx, in, now)
}

// checkNoticeAgainstActive compares an increase with the rule effective at the
// server's now, not with the row being patched.
func (s *Service) checkNoticeAgainstActive(ctx context.Context, tx *sql.Tx, in priceInput, now time.Time) *apierr.APIError {
	cur, err := activePriceRule(ctx, tx, in.modelID, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	if !isPriceIncrease(in, cur) {
		return nil
	}
	earliest := now.Add(priceNoticePeriod)
	if in.effectiveFrom.Before(earliest) {
		return apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("涨价需提前 7 天公示，最早可生效时间 %s（请求的 effective_from 为 %s）",
				earliest.Format(time.RFC3339), in.effectiveFrom.UTC().Format(time.RFC3339)))
	}
	return nil
}

// checkMarginFloor is the A11 gate. 3*P*r >= 40000*C is evaluated with
// integers only; the rendered figures use ceil(4C/3) so the operator sees the
// exact shortfall.
func (s *Service) checkMarginFloor(ctx context.Context, q priceDB, in priceInput) *apierr.APIError {
	type dimension struct {
		field string
		price int64
		cost  *int64
		unit  string
	}
	var dims []dimension
	switch in.modelType {
	case "TEXT":
		dims = []dimension{
			{"in_price_micro_per_k", in.inPrice, in.costIn, "微积分/千 token"},
			{"out_price_micro_per_k", in.outPrice, in.costOut, "微积分/千 token"},
		}
	case "IMAGE":
		dims = []dimension{{"per_call_price_micro", in.perCall, in.costCall, "微积分/张"}}
	case "EMBEDDING":
		dims = []dimension{{"in_price_micro_per_k", in.inPrice, in.costIn, "微积分/千 token"}}
	}

	var missing []string
	for _, d := range dims {
		if d.cost == nil {
			missing = append(missing, d.field+" 对应的成本价")
		}
	}
	if len(missing) > 0 {
		if !in.confirmNoCost {
			return apierr.Newf(apierr.CodeInvalidRequestBody,
				fmt.Sprintf("缺少成本价（%s），无法校验毛利地板；确认按无成本价保存请传 confirm_no_cost=true",
					strings.Join(missing, "、")))
		}
		// The operator explicitly accepted the warning. The audit row records
		// confirm_no_cost=true, so the exception is never silent, and the
		// warning also reaches the operator log. Dimensions that do have a
		// cost are still floor-checked below.
		s.logWarn(ctx, fmt.Sprintf("ops_price_no_cost model=%d fields=%s",
			in.modelID, strings.Join(missing, ",")))
	}

	groups, err := enabledGroupRatios(ctx, q)
	if err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	for _, d := range dims {
		if d.cost == nil || *d.cost == 0 {
			continue
		}
		for _, g := range groups {
			if g.ratioBP <= 0 {
				// internal (ratio 0) is the documented exception: it does not
				// participate in the floor (T15 hard constraint 5).
				continue
			}
			lhs := 3 * d.price * g.ratioBP
			rhs := floorCrossFactor * *d.cost
			if lhs >= rhs {
				continue
			}
			actual := d.price * g.ratioBP / groupRatioScale
			required := (floorCrossFactor**d.cost + floorScale - 1) / floorScale
			return apierr.Newf(apierr.CodeInvalidRequestBody,
				fmt.Sprintf("%s：分组 %s（倍率 %d）下 标准单价×倍率 = %d %s，低于成本 %d × 4/3 = %d（%d < %d），差额 %d",
					d.field, g.code, g.ratioBP, actual, d.unit, *d.cost, required,
					actual, required, required-actual))
		}
	}
	return nil
}

// checkPriceConflicts implements A12 without blocking the normal changeover.
//
// Overlapping rows split into two groups:
//
//   - rows starting before the new window are predecessors. A single price
//     among them is the current price being replaced; closePredecessors ends
//     it at the new start, so it is not a conflict. Two predecessors that
//     already disagree with each other are a pre-existing conflict and are
//     refused.
//   - rows starting at or after the new start would be effective together with
//     the new row, so any price difference is a conflict.
func (s *Service) checkPriceConflicts(ctx context.Context, tx *sql.Tx, in priceInput, selfID int64) *apierr.APIError {
	others, err := overlappingPriceRules(ctx, tx, in, selfID)
	if err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	want := priceRuleFromInput(in).signatureFull()

	var (
		conflicts       []string
		predecessorSigs = map[string]bool{}
		predecessors    []priceRow
	)
	for _, other := range others {
		if !other.EffectiveFrom.Before(in.effectiveFrom) {
			// Same or later start: both rows would be live at once.
			if other.signatureFull() != want {
				conflicts = append(conflicts, fmt.Sprintf("t_price_rule#%d", other.ID))
			}
			continue
		}
		predecessors = append(predecessors, other)
		predecessorSigs[other.signatureFull()] = true
	}
	if len(predecessorSigs) > 1 {
		// The database already holds two different prices for the same window.
		// Do not silently pick one: name every predecessor plus the new row.
		for _, p := range predecessors {
			conflicts = append(conflicts, fmt.Sprintf("t_price_rule#%d", p.ID))
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return apierr.Newf(apierr.CodePriceConflict,
		fmt.Sprintf("%s 与 t_price_rule#%d", strings.Join(conflicts, "、"), selfID))
}

// closePredecessors ends the currently effective rule at the new rule's start.
// It only runs after the conflict check, so all overlapping rows carry the
// same price; closing keeps the effective window single-valued without losing
// history.
func closePredecessors(ctx context.Context, tx *sql.Tx, in priceInput, selfID int64) *apierr.APIError {
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_price_rule
		   SET effective_to = ?
		 WHERE scope_level = 'MODEL' AND scope_id = ? AND enabled = 1 AND id <> ?
		   AND effective_from <= ?
		   AND (effective_to IS NULL OR effective_to > ?)`,
		in.effectiveFrom, in.modelID, selfID, in.effectiveFrom, in.effectiveFrom); err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return nil
}

func overlappingPriceRules(ctx context.Context, q priceDB, in priceInput, selfID int64) ([]priceRow, error) {
	to := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if in.effectiveTo != nil {
		to = *in.effectiveTo
	}
	rows, err := q.QueryContext(ctx, `
		SELECT `+priceSelectColumns+`
		  FROM t_price_rule p JOIN t_model m ON m.id = p.scope_id
		 WHERE p.scope_level = 'MODEL' AND p.scope_id = ? AND p.enabled = 1 AND p.id <> ?
		   AND p.effective_from < ?
		   AND (p.effective_to IS NULL OR p.effective_to > ?)
		 FOR UPDATE`, in.modelID, selfID, to, in.effectiveFrom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []priceRow
	for rows.Next() {
		row, err := scanPriceRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func activePriceRule(ctx context.Context, q priceDB, modelID int64, now time.Time) (priceRow, error) {
	row := q.QueryRowContext(ctx, `
		SELECT `+priceSelectColumns+`
		  FROM t_price_rule p JOIN t_model m ON m.id = p.scope_id
		 WHERE p.scope_level = 'MODEL' AND p.scope_id = ? AND p.enabled = 1
		   AND p.effective_from <= ? AND (p.effective_to IS NULL OR p.effective_to > ?)
		 ORDER BY p.version DESC, p.id DESC LIMIT 1`, modelID, now, now)
	return scanPriceRule(row)
}

func nextPriceVersion(ctx context.Context, q priceDB, modelID int64) (int64, error) {
	var version int64
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM t_price_rule
		 WHERE scope_level = 'MODEL' AND scope_id = ?`, modelID).Scan(&version)
	return version, err
}

func (s *Service) modelTypeTx(ctx context.Context, q priceDB, modelID int64) (string, *apierr.APIError) {
	var modelType string
	err := q.QueryRowContext(ctx,
		`SELECT model_type FROM t_model WHERE id = ? AND deleted_at IS NULL`, modelID).Scan(&modelType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		return "", apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return modelType, nil
}

type priceDB interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type groupRatioRow struct {
	code    string
	ratioBP int64
}

func enabledGroupRatios(ctx context.Context, q priceDB) ([]groupRatioRow, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT code, ratio_bp FROM t_price_group WHERE enabled = 1 ORDER BY ratio_bp, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []groupRatioRow
	for rows.Next() {
		var g groupRatioRow
		if err := rows.Scan(&g.code, &g.ratioBP); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

func scanPriceRule(row rowScanner) (priceRow, error) {
	var (
		v                         priceRow
		modelID                   int64
		cacheHit, cacheWrite      sql.NullInt64
		longThreshold             sql.NullInt64
		longIn, longOut           sql.NullInt64
		costIn, costOut, costCall sql.NullInt64
		effectiveFrom             time.Time
		effectiveTo               sql.NullTime
		createdAt                 time.Time
	)
	if err := row.Scan(
		&v.ID, &v.ScopeLevel, &modelID, &v.ModelName, &v.ModelType,
		&v.InPriceMicroPerK, &v.OutPriceMicroPerK, &v.PerCallPriceMicro,
		&cacheHit, &cacheWrite, &longThreshold, &longIn, &longOut,
		&costIn, &costOut, &costCall, &v.CurrencyCost, &v.Version,
		&effectiveFrom, &effectiveTo, &v.Enabled, &createdAt,
	); err != nil {
		return priceRow{}, err
	}
	v.ModelID = modelID
	v.CacheHitRatioBP = nullableInt64Ptr(cacheHit)
	v.CacheWriteRatioBP = nullableInt64Ptr(cacheWrite)
	v.LongCtxThresholdTokens = nullableInt64Ptr(longThreshold)
	v.LongCtxInPriceMicroPerK = nullableInt64Ptr(longIn)
	v.LongCtxOutPriceMicroPerK = nullableInt64Ptr(longOut)
	v.CostInMicroPerK = nullableInt64Ptr(costIn)
	v.CostOutMicroPerK = nullableInt64Ptr(costOut)
	v.CostPerCallMicro = nullableInt64Ptr(costCall)
	v.EffectiveFrom = effectiveFrom.UTC()
	v.EffectiveTo = nullableTime(effectiveTo)
	v.CreatedAt = createdAt.UTC()
	return v, nil
}

func loadPriceRuleTx(ctx context.Context, q priceDB, id int64, forUpdate bool) (priceRow, *apierr.APIError) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	row := q.QueryRowContext(ctx, `
		SELECT `+priceSelectColumns+`
		  FROM t_price_rule p JOIN t_model m ON m.id = p.scope_id
		 WHERE p.id = ? AND p.scope_level = 'MODEL'`+suffix, id)
	v, err := scanPriceRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return priceRow{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		return priceRow{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return v, nil
}

func priceAuditView(p priceRow) map[string]any {
	return map[string]any{
		"model_id":                  p.ModelID,
		"model_name":                p.ModelName,
		"model_type":                p.ModelType,
		"in_price_micro_per_k":      p.InPriceMicroPerK,
		"out_price_micro_per_k":     p.OutPriceMicroPerK,
		"per_call_price_micro":      p.PerCallPriceMicro,
		"cache_hit_ratio_bp":        p.CacheHitRatioBP,
		"cache_write_ratio_bp":      p.CacheWriteRatioBP,
		"long_ctx_threshold_tokens": p.LongCtxThresholdTokens,
		"long_ctx_in_price_micro":   p.LongCtxInPriceMicroPerK,
		"long_ctx_out_price_micro":  p.LongCtxOutPriceMicroPerK,
		"cost_in_micro_per_k":       p.CostInMicroPerK,
		"cost_out_micro_per_k":      p.CostOutMicroPerK,
		"cost_per_call_micro":       p.CostPerCallMicro,
		"currency_cost":             p.CurrencyCost,
		"version":                   p.Version,
		"effective_from":            p.EffectiveFrom,
		"effective_to":              p.EffectiveTo,
		"enabled":                   p.Enabled,
	}
}

func hasPricePatch(req *priceWriteRequest) bool {
	return req.InPriceMicroPerK != nil || req.OutPriceMicroPerK != nil ||
		req.PerCallPriceMicro != nil || req.CacheHitRatioBP != nil ||
		req.CacheWriteRatioBP != nil || req.LongCtxThresholdTokens != nil ||
		req.LongCtxInPriceMicroPerK != nil || req.LongCtxOutPriceMicroPerK != nil ||
		req.CostInMicroPerK != nil || req.CostOutMicroPerK != nil ||
		req.CostPerCallMicro != nil || req.CurrencyCost != nil ||
		req.EffectiveFrom != nil || req.EffectiveTo != nil || req.Enabled != nil
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullTimeValue(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC()
}

func parseInt64Query(raw string) (int64, error) {
	return strconv.ParseInt(raw, 10, 64)
}
