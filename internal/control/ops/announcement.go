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
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
)

// Announcement states. DRAFT, PUBLISHED and OFFLINE are the TDS enum.
// SCHEDULED is an internal marker for F17-2 "到点自动发布": without a stored
// marker, a plain draft and a scheduled notice are indistinguishable, and the
// scheduler would publish drafts an operator has not approved.
const (
	annDraft     = "DRAFT"
	annScheduled = "SCHEDULED"
	annPublished = "PUBLISHED"
	annOffline   = "OFFLINE"
)

const (
	annTypeNotice      = "NOTICE"
	annTypeMaintenance = "MAINTENANCE"
	annTypePromotion   = "PROMOTION"
	annTypePriceChange = "PRICE_CHANGE"
)

type announcementView struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	Content       string     `json:"content"`
	AnnType       string     `json:"ann_type"`
	PositionMask  int64      `json:"position_mask"`
	Pinned        bool       `json:"pinned"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	State         string     `json:"state"`
	CreatedBy     int64      `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type announcementListResponse struct {
	Items    []announcementView `json:"items"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Total    int64              `json:"total"`
}

type announcementDetailResponse struct {
	Announcement announcementView `json:"announcement"`
}

type announcementWriteRequest struct {
	Title        *string    `json:"title"`
	Content      *string    `json:"content"`
	AnnType      *string    `json:"ann_type"`
	PositionMask *int64     `json:"position_mask"`
	Pinned       *bool      `json:"pinned"`
	PublishAt    *time.Time `json:"publish_at"`
	EffectiveTo  *time.Time `json:"effective_to"`
	// ModelIDs only narrows the F16-4 promotion conflict check. t_announcement
	// has no model column, so it is not persisted; an empty list means "check
	// every model".
	ModelIDs []int64 `json:"model_ids"`
}

type announcementPublishRequest struct {
	PublishAt *time.Time `json:"publish_at"`
}

const announcementSelectColumns = `
	a.id, a.title, a.content, a.ann_type, a.position_mask, a.pinned,
	a.effective_from, a.effective_to, a.state, a.created_by, a.created_at, a.updated_at`

// ListAnnouncements implements GET /api/admin/announcements.
func (s *Service) ListAnnouncements(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r, "created_at", "updated_at", "effective_from", "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := r.URL.Query()
	where := []string{"a.deleted_at IS NULL"}
	args := make([]any, 0, 4)
	if raw := strings.ToUpper(strings.TrimSpace(q.Get("state"))); raw != "" {
		if raw != annDraft && raw != annScheduled && raw != annPublished && raw != annOffline {
			mw.WriteError(w, r, mw.InvalidQueryField("state"))
			return
		}
		where = append(where, "a.state = ?")
		args = append(args, raw)
	}
	if raw := strings.ToUpper(strings.TrimSpace(q.Get("ann_type"))); raw != "" {
		if !validAnnType(raw) {
			mw.WriteError(w, r, mw.InvalidQueryField("ann_type"))
			return
		}
		where = append(where, "a.ann_type = ?")
		args = append(args, raw)
	}
	if raw := strings.TrimSpace(q.Get("pinned")); raw != "" {
		if raw != "0" && raw != "1" {
			mw.WriteError(w, r, mw.InvalidQueryField("pinned"))
			return
		}
		where = append(where, "a.pinned = ?")
		args = append(args, raw)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM t_announcement a WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(r.Context(), "ops_announcement_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	orderBy := "a.created_at"
	switch page.Sort {
	case "updated_at":
		orderBy = "a.updated_at"
	case "effective_from":
		orderBy = "a.effective_from"
	case "id":
		orderBy = "a.id"
	}
	dir := orderDirection(page.Order)
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT `+announcementSelectColumns+`
		  FROM t_announcement a
		 WHERE `+whereSQL+`
		 ORDER BY `+orderBy+` `+dir+`, a.id `+dir+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(r.Context(), "ops_announcement_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]announcementView, 0, page.Size)
	for rows.Next() {
		v, err := scanAnnouncement(rows)
		if err != nil {
			s.logError(r.Context(), "ops_announcement_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "ops_announcement_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, announcementListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// CreateAnnouncement implements POST /api/admin/announcements.
func (s *Service) CreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req announcementWriteRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	in, state, apiErr := s.validateAnnouncement(r.Context(), &req, nil)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_announcement_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_announcement
			(title, content, ann_type, position_mask, pinned, effective_from,
			 effective_to, state, created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		in.title, in.content, in.annType, in.positionMask, in.pinned,
		in.effectiveFrom, nullTimeValue(in.effectiveTo), state, mustActorID(r), now, now)
	if err != nil {
		s.logError(ctx, "ops_announcement_insert_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		s.logError(ctx, "ops_announcement_id_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	after, apiErr := loadAnnouncementTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	action := "CREATE"
	if state == annPublished || state == annScheduled {
		action = "PUBLISH"
	}
	if err := s.insertAudit(ctx, tx, r, "ANNOUNCEMENT", action, "t_announcement", id,
		nil, announcementAuditView(after), ""); err != nil {
		s.logError(ctx, "ops_announcement_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_announcement_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusCreated, announcementDetailResponse{Announcement: after})
}

// UpdateAnnouncement implements PATCH /api/admin/announcements/{id}.
func (s *Service) UpdateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req announcementWriteRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if !hasAnnouncementPatch(&req) {
		mw.WriteError(w, r, mw.InvalidField("body"))
		return
	}

	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_announcement_patch_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	before, apiErr := loadAnnouncementTx(ctx, tx, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if before.State == annPublished || before.State == annOffline {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			fmt.Sprintf("公告当前状态为 %s，已发布/已下线内容不可编辑", before.State)))
		return
	}
	in, state, apiErr := s.validateAnnouncement(ctx, &req, &before)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_announcement
		   SET title = ?, content = ?, ann_type = ?, position_mask = ?, pinned = ?,
		       effective_from = ?, effective_to = ?, state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		in.title, in.content, in.annType, in.positionMask, in.pinned,
		in.effectiveFrom, nullTimeValue(in.effectiveTo), state, now, id); err != nil {
		s.logError(ctx, "ops_announcement_update_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	after, apiErr := loadAnnouncementTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "ANNOUNCEMENT", "UPDATE", "t_announcement", id,
		announcementAuditView(before), announcementAuditView(after), ""); err != nil {
		s.logError(ctx, "ops_announcement_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_announcement_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusOK, announcementDetailResponse{Announcement: after})
}

// PublishAnnouncement implements POST /api/admin/announcements/{id}/publish.
//
// Publishing only moves t_announcement.state. It never re-reads or writes
// t_model.listing_state (PRD F17-3, T15 hard constraint 12): the announcement
// is a notification, not a state-machine driver.
func (s *Service) PublishAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req announcementPublishRequest
	if r.ContentLength > 0 {
		if apiErr := decodeJSON(w, r, &req); apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
	}
	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_announcement_publish_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	before, apiErr := loadAnnouncementTx(ctx, tx, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if before.State == annPublished {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			"公告当前状态为 PUBLISHED，无需重复发布"))
		return
	}
	if before.AnnType == annTypePromotion {
		if apiErr := s.checkPromotionConflict(ctx, tx, nil, now); apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
	}

	nextState := annPublished
	effectiveFrom := now
	if req.PublishAt != nil && req.PublishAt.After(now) {
		nextState = annScheduled
		effectiveFrom = req.PublishAt.UTC()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_announcement
		   SET state = ?, effective_from = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`, nextState, effectiveFrom, now, id); err != nil {
		s.logError(ctx, "ops_announcement_publish_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	after, apiErr := loadAnnouncementTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "ANNOUNCEMENT", "PUBLISH", "t_announcement", id,
		announcementAuditView(before), announcementAuditView(after), ""); err != nil {
		s.logError(ctx, "ops_announcement_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "ops_announcement_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	committed = true
	_ = mw.WriteJSON(w, http.StatusOK, announcementDetailResponse{Announcement: after})
}

// PublishDueAnnouncements is the F17-2 scheduler step. It publishes every
// SCHEDULED row whose effective_from has arrived. The clock is injected
// (Service.Now), so the test can advance time without sleeping.
//
// It is deliberately not a blanket "publish everything due": a DRAFT row may
// have effective_from in the past and still await operator approval.
func (s *Service) PublishDueAnnouncements(ctx context.Context) (int, error) {
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM t_announcement
		 WHERE state = ? AND deleted_at IS NULL AND effective_from <= ?
		 ORDER BY id FOR UPDATE`, annScheduled, now)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, 8)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE t_announcement SET state = ?, updated_at = ? WHERE id = ?`,
			annPublished, now, id); err != nil {
			return 0, err
		}
		if s.Audit != nil {
			before, _, err := audit.MarshalBeforeAfter(nil, map[string]any{"state": annScheduled})
			if err != nil {
				return 0, err
			}
			_, after, err := audit.MarshalBeforeAfter(nil, map[string]any{"state": annPublished})
			if err != nil {
				return 0, err
			}
			if err := s.Audit.Insert(ctx, tx, audit.Entry{
				OperatorID:   0,
				OperatorRole: "SYSTEM",
				Module:       "ANNOUNCEMENT",
				Action:       "AUTO_PUBLISH",
				TargetType:   "t_announcement",
				TargetID:     fmt.Sprintf("%d", id),
				Before:       before,
				After:        after,
				Result:       "OK",
			}); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return len(ids), nil
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

type announcementInput struct {
	title         string
	content       string
	annType       string
	positionMask  int64
	pinned        bool
	effectiveFrom time.Time
	effectiveTo   *time.Time
}

func (s *Service) validateAnnouncement(ctx context.Context, req *announcementWriteRequest, cur *announcementView) (announcementInput, string, *apierr.APIError) {
	var in announcementInput
	now := s.now()
	nextState := annDraft
	if cur != nil {
		in.title = cur.Title
		in.content = cur.Content
		in.annType = cur.AnnType
		in.positionMask = cur.PositionMask
		in.pinned = cur.Pinned
		in.effectiveFrom = cur.EffectiveFrom
		in.effectiveTo = cur.EffectiveTo
		nextState = cur.State
	}

	if req.Title != nil {
		in.title = strings.TrimSpace(*req.Title)
	}
	if in.title == "" || len([]rune(in.title)) > 128 {
		return in, "", mw.InvalidField("title")
	}
	if req.Content != nil {
		in.content = strings.TrimSpace(*req.Content)
	}
	if in.content == "" || len([]rune(in.content)) > 20000 {
		return in, "", mw.InvalidField("content")
	}
	if req.AnnType != nil {
		in.annType = strings.ToUpper(strings.TrimSpace(*req.AnnType))
	}
	if !validAnnType(in.annType) {
		return in, "", mw.InvalidField("ann_type")
	}
	if req.PositionMask != nil {
		in.positionMask = *req.PositionMask
	}
	if in.positionMask < 1 || in.positionMask > 15 {
		return in, "", mw.InvalidField("position_mask")
	}
	if req.Pinned != nil {
		in.pinned = *req.Pinned
	}
	if req.EffectiveTo != nil {
		in.effectiveTo = req.EffectiveTo
	}

	// publish_at decides DRAFT / SCHEDULED / PUBLISHED on create. On patch it
	// reschedules an existing draft; editing content alone never publishes.
	if req.PublishAt != nil {
		at := req.PublishAt.UTC()
		in.effectiveFrom = at
		if at.After(now) {
			nextState = annScheduled
		} else {
			nextState = annPublished
			in.effectiveFrom = now
		}
	} else if cur == nil {
		in.effectiveFrom = now
	}
	if in.effectiveTo != nil {
		to := in.effectiveTo.UTC()
		in.effectiveTo = &to
		if !to.After(in.effectiveFrom) {
			return in, "", mw.InvalidField("effective_to")
		}
	}

	// F16-4: an activity notice may not collide with a pending price increase.
	if in.annType == annTypePromotion {
		if apiErr := s.checkPromotionConflict(ctx, s.DB, req.ModelIDs, now); apiErr != nil {
			return in, "", apiErr
		}
	}
	return in, nextState, nil
}

func validAnnType(v string) bool {
	switch v {
	case annTypeNotice, annTypeMaintenance, annTypePromotion, annTypePriceChange:
		return true
	}
	return false
}

// checkPromotionConflict refuses an activity notice while a price increase is
// inside its seven-day public-notice window. modelIDs narrows the check; an
// empty list checks every model, because t_announcement has no model column
// and the safe default is to block the conflicting activity.
func (s *Service) checkPromotionConflict(ctx context.Context, q priceDB, modelIDs []int64, now time.Time) *apierr.APIError {
	where := []string{"p.scope_level = 'MODEL'", "p.enabled = 1", "m.deleted_at IS NULL",
		"p.effective_from > ?", "p.effective_from <= ?"}
	args := []any{now, now.Add(priceNoticePeriod)}
	if len(modelIDs) > 0 {
		for _, id := range modelIDs {
			if id <= 0 {
				return mw.InvalidField("model_ids")
			}
		}
		where = append(where, "p.scope_id IN ("+placeholders(len(modelIDs))+")")
		for _, id := range modelIDs {
			args = append(args, id)
		}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT `+priceSelectColumns+`
		  FROM t_price_rule p JOIN t_model m ON m.id = p.scope_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY p.effective_from`, args...)
	if err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	defer rows.Close()
	for rows.Next() {
		future, err := scanPriceRule(rows)
		if err != nil {
			return apierr.New(apierr.CodeInternalError).WithCause(err)
		}
		cur, err := activePriceRule(ctx, q, future.ModelID, now)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return apierr.New(apierr.CodeInternalError).WithCause(err)
		}
		if !isPriceIncrease(priceInput{
			inPrice:  future.InPriceMicroPerK,
			outPrice: future.OutPriceMicroPerK,
			perCall:  future.PerCallPriceMicro,
			longIn:   future.LongCtxInPriceMicroPerK,
			longOut:  future.LongCtxOutPriceMicroPerK,
		}, cur) {
			continue
		}
		return apierr.Newf(apierr.CodeInvalidRequestBody,
			fmt.Sprintf("模型 %s 的涨价将于 %s 生效，7 天公示期内不得发布「活动」类公告；请改用 PRICE_CHANGE/NOTICE 类型或等公示期结束",
				future.ModelName, future.EffectiveFrom.UTC().Format(time.RFC3339)))
	}
	if err := rows.Err(); err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return nil
}

func hasAnnouncementPatch(req *announcementWriteRequest) bool {
	return req.Title != nil || req.Content != nil || req.AnnType != nil ||
		req.PositionMask != nil || req.Pinned != nil || req.PublishAt != nil ||
		req.EffectiveTo != nil
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

func scanAnnouncement(row rowScanner) (announcementView, error) {
	var (
		v             announcementView
		effectiveTo   sql.NullTime
		effectiveFrom time.Time
		createdAt     time.Time
		updatedAt     time.Time
	)
	if err := row.Scan(&v.ID, &v.Title, &v.Content, &v.AnnType, &v.PositionMask,
		&v.Pinned, &effectiveFrom, &effectiveTo, &v.State, &v.CreatedBy,
		&createdAt, &updatedAt); err != nil {
		return announcementView{}, err
	}
	v.EffectiveFrom = effectiveFrom.UTC()
	v.EffectiveTo = nullableTime(effectiveTo)
	v.CreatedAt = createdAt.UTC()
	v.UpdatedAt = updatedAt.UTC()
	return v, nil
}

func loadAnnouncementTx(ctx context.Context, q priceDB, id int64, forUpdate bool) (announcementView, *apierr.APIError) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	v, err := scanAnnouncement(q.QueryRowContext(ctx, `
		SELECT `+announcementSelectColumns+`
		  FROM t_announcement a
		 WHERE a.id = ? AND a.deleted_at IS NULL`+suffix, id))
	if errors.Is(err, sql.ErrNoRows) {
		return announcementView{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		return announcementView{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return v, nil
}

func announcementAuditView(v announcementView) map[string]any {
	return map[string]any{
		"title":          v.Title,
		"ann_type":       v.AnnType,
		"position_mask":  v.PositionMask,
		"pinned":         v.Pinned,
		"effective_from": v.EffectiveFrom,
		"effective_to":   v.EffectiveTo,
		"state":          v.State,
	}
}
