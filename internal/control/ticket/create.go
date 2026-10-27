package ticket

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// hitRecord is the subset of t_content_policy_hit a content appeal needs. The
// rule id is read here and never accepted from the request body.
type hitRecord struct {
	ID        int64
	RequestID string
	RuleID    string
	Category  string
	Action    string
	CreatedAt time.Time
}

// Create implements POST /api/console/tickets (F19-1 / F19-3).
//
// request_id is optional for BILLING / TECH / OTHER, but when supplied it must
// be the caller's own request from the last 90 days. A foreign id, an expired
// id and an id that never existed all answer the same resource_not_found body:
// telling them apart would confirm that someone else's request exists.
//
// CONTENT_APPEAL is the exception: a policy-blocked request never reaches
// t_usage_record (TDS §8.4.2 forbids writing a billing row for it), so the
// rule id and request_id are derived from t_content_policy_hit instead.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req createTicketRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	typ := strings.ToUpper(strings.TrimSpace(req.TicketType))
	if !validType(typ) {
		mw.WriteError(w, r, mw.InvalidField("ticket_type"))
		return
	}
	title := normalizeText(req.Title, false)
	if n := len([]rune(title)); n == 0 || n > maxTitleRunes {
		mw.WriteError(w, r, mw.InvalidField("title"))
		return
	}
	description := normalizeText(req.Description, true)
	if n := len([]rune(description)); n == 0 || n > maxDescriptionRunes {
		mw.WriteError(w, r, mw.InvalidField("description"))
		return
	}

	ctx := r.Context()
	now := s.now()
	requestID := strings.TrimSpace(req.RequestID)

	var (
		hitID  *int64
		hitRow hitRecord
		source string
		notice string
	)
	if typ == TypeContentAppeal {
		if req.PolicyHitID == nil && requestID == "" {
			// The hit reference is the one thing the user must supply; the
			// rule id itself is derived. Fixable by editing the body.
			mw.WriteError(w, r, mw.InvalidField("request_id"))
			return
		}
		rec, apiErr := s.resolveAppealHit(ctx, uid, req.PolicyHitID, requestID)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		if apiErr := appealEligible(rec, now); apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		if requestID != "" && requestID != rec.RequestID {
			// The hit record is the evidence; its request_id wins. Say so
			// instead of silently discarding what the user typed.
			notice = "已按命中的违规记录校正关联 request_id（以命中记录为准）"
		}
		hitRow = rec
		id := rec.ID
		hitID = &id
		requestID = rec.RequestID
		source = "POLICY_HIT"
	} else {
		if req.PolicyHitID != nil {
			mw.WriteError(w, r, mw.InvalidField("policy_hit_id"))
			return
		}
		if requestID != "" {
			if apiErr := s.checkOwnRequest(ctx, uid, requestID, now); apiErr != nil {
				mw.WriteError(w, r, apiErr)
				return
			}
			source = "USER"
		}
	}

	var reqArg, hitArg any
	if requestID != "" {
		reqArg = requestID
	}
	if hitID != nil {
		hitArg = *hitID
	}

	var ticketID int64
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

		inserted := false
		for attempt := 0; attempt < ticketNoMaxAttempts && !inserted; attempt++ {
			res, execErr := tx.ExecContext(ctx, `
				INSERT INTO t_ticket
					(ticket_no, user_id, ticket_type, title, description,
					 request_id, policy_hit_id, state, first_response_due_at,
					 first_responded_at, last_replied_at, reopen_count,
					 created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,NULL,NULL,0,?,?)`,
				ticketNo(now, randomDigits), uid, typ, title, description,
				reqArg, hitArg, StateOpen, now.Add(firstResponseSLA), now, now)
			if execErr != nil {
				if isDuplicateKey(execErr) {
					continue
				}
				return execErr
			}
			ticketID, err = res.LastInsertId()
			if err != nil {
				return err
			}
			inserted = true
		}
		if !inserted {
			return errors.New("ticket: could not allocate a unique ticket_no")
		}

		if typ == TypeContentAppeal {
			// CAS on action='BLOCK': if another appeal changed the row after
			// resolveAppealHit read it, this update affects zero rows and the
			// whole ticket rolls back. The hit row is the appeal's evidence,
			// so the two writes must agree.
			res, execErr := tx.ExecContext(ctx, `
				UPDATE t_content_policy_hit
				   SET action = ?, appeal_at = ?, appeal_note = ?
				 WHERE id = ? AND user_id = ? AND action = ?`,
				hitActionAppealPending, now, truncateRunes(description, 500),
				hitRow.ID, uid, hitActionBlock)
			if execErr != nil {
				return execErr
			}
			n, rowsErr := res.RowsAffected()
			if rowsErr != nil {
				return rowsErr
			}
			if n == 0 {
				return errAppealStateChanged
			}
		}

		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}()
	if errors.Is(err, errAppealStateChanged) {
		mw.WriteError(w, r, apierr.Newf(apierr.CodeResourceStateConflict,
			"该请求的申诉状态已变更，请刷新后重试"))
		return
	}
	if err != nil {
		s.logError(ctx, "ticket_create_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	row, apiErr := s.loadUserTicket(ctx, uid, ticketID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	view, apiErr := s.toView(ctx, row, false)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusCreated, createTicketResponse{
		Ticket:          view,
		RequestIDSource: source,
		Notice:          notice,
	})
}

var errAppealStateChanged = errors.New("ticket: content policy hit is no longer appealable")

// resolveAppealHit finds the hit row the appeal is about. Both branches filter
// by user_id, so a foreign hit and a nonexistent hit are indistinguishable.
func (s *Service) resolveAppealHit(ctx context.Context, uid int64, hitID *int64, requestID string) (hitRecord, *apierr.APIError) {
	var row *sql.Row
	if hitID != nil {
		if *hitID <= 0 {
			return hitRecord{}, mw.InvalidField("policy_hit_id")
		}
		row = s.DB.QueryRowContext(ctx, `SELECT id, request_id, rule_id, category, action, created_at
			  FROM t_content_policy_hit WHERE id = ? AND user_id = ?`, *hitID, uid)
	} else {
		// t_usage_record and t_content_policy_hit both declare char(36). A
		// malformed value can never match a row, and comparing a longer value
		// against the column is a different error class, so answer the same
		// 404 an absent id gets.
		if len(requestID) != 36 {
			return hitRecord{}, apierr.New(apierr.CodeResourceNotFound)
		}
		row = s.DB.QueryRowContext(ctx, `SELECT id, request_id, rule_id, category, action, created_at
			  FROM t_content_policy_hit WHERE request_id = ? AND user_id = ?`, requestID, uid)
	}
	var rec hitRecord
	err := row.Scan(&rec.ID, &rec.RequestID, &rec.RuleID, &rec.Category, &rec.Action, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Same code and message as a nonexistent request (hard constraint 3).
		return hitRecord{}, apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		s.logError(ctx, "ticket_appeal_hit_read_failed: "+err.Error())
		return hitRecord{}, apierr.New(apierr.CodeInternalError)
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return rec, nil
}

// appealEligible enforces the two conditions TDS §8.4.5 puts on an appeal:
// the hit must still be in BLOCK state, and the decision must be at most 30
// days old. Both are state refusals of an existing, owned hit — a 409, not a
// 400, because editing the body cannot fix either.
func appealEligible(rec hitRecord, now time.Time) *apierr.APIError {
	switch rec.Action {
	case hitActionBlock:
	case hitActionAppealPending:
		return apierr.Newf(apierr.CodeResourceStateConflict, "该请求的申诉正在处理中")
	case hitActionAppealUpheld:
		return apierr.Newf(apierr.CodeResourceStateConflict, "该请求的申诉已复核，维持拦截")
	case hitActionAppealOverturn:
		return apierr.Newf(apierr.CodeResourceStateConflict, "该请求的申诉已成立，无需再次提交")
	default:
		return apierr.Newf(apierr.CodeResourceStateConflict,
			"该请求当前状态（"+rec.Action+"）不可申诉")
	}
	if now.Sub(rec.CreatedAt) > appealWindow {
		return apierr.Newf(apierr.CodeResourceStateConflict,
			"已超过 30 天申诉期，无法提交内容申诉")
	}
	return nil
}

// checkOwnRequest verifies that request_id is the caller's own request from
// the last 90 days. A length check runs first because char(36) comparison of a
// longer value would be a different kind of error in strict mode.
func (s *Service) checkOwnRequest(ctx context.Context, uid int64, requestID string, now time.Time) *apierr.APIError {
	if len(requestID) != 36 {
		return apierr.New(apierr.CodeResourceNotFound)
	}
	var one int
	err := s.DB.QueryRowContext(ctx, `
		SELECT 1 FROM t_usage_record
		 WHERE request_id = ? AND user_id = ? AND created_at >= ?
		 LIMIT 1`, requestID, uid, now.Add(-pickerWindow)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return apierr.New(apierr.CodeResourceNotFound)
	}
	if err != nil {
		s.logError(ctx, "ticket_request_lookup_failed: "+err.Error())
		return apierr.New(apierr.CodeInternalError)
	}
	return nil
}
