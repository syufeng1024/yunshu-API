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
	// ModelIDs comes from t_announcement_model. It is always an array: an
	// announcement without model associations returns [] rather than null so
	// the console can distinguish "no association" from "field missing".
	ModelIDs  []int64   `json:"model_ids"`
	CreatedBy int64     `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	Title         *string    `json:"title"`
	Content       *string    `json:"content"`
	AnnType       *string    `json:"ann_type"`
	PositionMask  *int64     `json:"position_mask"`
	Pinned        *bool      `json:"pinned"`
	PublishAt     *time.Time `json:"publish_at"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	// ModelIDs is persisted in t_announcement_model. It also narrows the F16-4
	// promotion conflict check; an empty array means "no association" (the
	// conflict check then covers every model).
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
	if err := attachAnnouncementModels(r.Context(), s.DB, items); err != nil {
		s.logError(r.Context(), "ops_announcement_models_failed: "+err.Error())
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
	modelIDs, apiErr := s.normalizeAnnouncementModelIDs(r.Context(), s.DB, req.ModelIDs)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	req.ModelIDs = modelIDs
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
	if apiErr := replaceAnnouncementModels(ctx, tx, id, modelIDs); apiErr != nil {
		mw.WriteError(w, r, apiErr)
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
	replaceModels := req.ModelIDs != nil
	if replaceModels {
		modelIDs, apiErr := s.normalizeAnnouncementModelIDs(ctx, tx, req.ModelIDs)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		req.ModelIDs = modelIDs
	} else {
		// An update that does not mention model_ids keeps the existing
		// associations. The promotion conflict check still needs them, so load
		// them without replacing the rows.
		existing, err := loadAnnouncementModelIDs(ctx, tx, id)
		if err != nil {
			s.logError(ctx, "ops_announcement_models_load_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		req.ModelIDs = existing
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
	if replaceModels {
		if apiErr := replaceAnnouncementModels(ctx, tx, id, req.ModelIDs); apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
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
		modelIDs, err := loadAnnouncementModelIDs(ctx, tx, id)
		if err != nil {
			s.logError(ctx, "ops_announcement_models_load_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		if apiErr := s.checkPromotionConflict(ctx, tx, modelIDs, now); apiErr != nil {
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

// OfflineAnnouncement implements POST /api/admin/announcements/{id}/offline.
//
// OFFLINE is a terminal display state, not a delete: the row and its audit trail
// stay readable. DRAFT / SCHEDULED / PUBLISHED may all be taken offline; a
// second call answers 409 instead of silently succeeding, because "already
// offline" is not the same operator intent as "take it offline now".
func (s *Service) OfflineAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, apiErr := pathID(r, "id")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "ops_announcement_offline_begin_failed: "+err.Error())
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
	if before.State == annOffline {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			"公告当前状态为 OFFLINE，无需重复下线"))
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_announcement
		   SET state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		annOffline, now, id); err != nil {
		s.logError(ctx, "ops_announcement_offline_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	after, apiErr := loadAnnouncementTx(ctx, tx, id, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if err := s.insertAudit(ctx, tx, r, "ANNOUNCEMENT", "OFFLINE", "t_announcement", id,
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
	// effective_from is the visibility start. When publish_at is also supplied,
	// a notice cannot become visible before it is published: publish_at is the
	// lower bound and a smaller effective_from is a body error naming the field.
	if req.EffectiveFrom != nil {
		from := req.EffectiveFrom.UTC()
		if from.IsZero() {
			return in, "", mw.InvalidField("effective_from")
		}
		if req.PublishAt != nil && from.Before(in.effectiveFrom) {
			return in, "", mw.InvalidField("effective_from")
		}
		in.effectiveFrom = from
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
		req.EffectiveFrom != nil || req.EffectiveTo != nil || req.ModelIDs != nil
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
	v.ModelIDs = []int64{}
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
	items := []announcementView{v}
	if err := attachAnnouncementModels(ctx, q, items); err != nil {
		return announcementView{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return items[0], nil
}

// attachAnnouncementModels fills model_ids for a whole page with one query.
// The alternative (one SELECT per announcement) is the N+1 the T24 card names.
func attachAnnouncementModels(ctx context.Context, q priceDB, items []announcementView) error {
	for i := range items {
		items[i].ModelIDs = []int64{}
	}
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	args := make([]any, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
		args = append(args, item.ID)
	}
	rows, err := q.QueryContext(ctx, `
		SELECT announcement_id, model_id
		  FROM t_announcement_model
		 WHERE announcement_id IN (`+placeholders(len(ids))+`)
		 ORDER BY announcement_id, model_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byAnnouncement := make(map[int64][]int64, len(ids))
	for rows.Next() {
		var announcementID, modelID int64
		if err := rows.Scan(&announcementID, &modelID); err != nil {
			return err
		}
		byAnnouncement[announcementID] = append(byAnnouncement[announcementID], modelID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		if models, ok := byAnnouncement[items[i].ID]; ok {
			items[i].ModelIDs = models
		}
	}
	return nil
}

func loadAnnouncementModelIDs(ctx context.Context, q priceDB, id int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT model_id FROM t_announcement_model
		 WHERE announcement_id = ? ORDER BY model_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0, 4)
	for rows.Next() {
		var modelID int64
		if err := rows.Scan(&modelID); err != nil {
			return nil, err
		}
		out = append(out, modelID)
	}
	return out, rows.Err()
}

// normalizeAnnouncementModelIDs deduplicates and verifies every id against
// t_model. A dangling association would make the read endpoint return a model
// id no console can resolve, so it is a body error rather than a silent skip.
func (s *Service) normalizeAnnouncementModelIDs(ctx context.Context, q priceDB, raw []int64) ([]int64, *apierr.APIError) {
	if raw == nil {
		return nil, nil
	}
	if len(raw) > 100 {
		return nil, mw.InvalidField("model_ids")
	}
	out := make([]int64, 0, len(raw))
	seen := make(map[int64]bool, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, mw.InvalidField("model_ids")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return out, nil
	}
	args := make([]any, len(out))
	for i, id := range out {
		args[i] = id
	}
	var found int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_model
		  WHERE id IN (`+placeholders(len(out))+`) AND deleted_at IS NULL`,
		args...).Scan(&found); err != nil {
		return nil, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	if found != len(out) {
		return nil, mw.InvalidField("model_ids")
	}
	return out, nil
}

func replaceAnnouncementModels(ctx context.Context, tx *sql.Tx, id int64, modelIDs []int64) *apierr.APIError {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM t_announcement_model WHERE announcement_id = ?`, id); err != nil {
		return apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	for _, modelID := range modelIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO t_announcement_model (announcement_id, model_id)
			VALUES (?,?)`, id, modelID); err != nil {
			return apierr.New(apierr.CodeInternalError).WithCause(err)
		}
	}
	return nil
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
		"model_ids":      v.ModelIDs,
	}
}
