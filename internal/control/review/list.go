package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// List implements GET /api/admin/reviews.
//
// F20-1: the default tab is PENDING and the order is submission time
// ascending. The handler does not accept a sort parameter at all — amount,
// priority and updated_at are deliberately unreachable as sort keys.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r) // no allowed sort columns: sort= is a 400
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := r.URL.Query()

	state := strings.ToUpper(strings.TrimSpace(q.Get("state")))
	if state == "" {
		state = StatePending // default tab
	}
	if state != "ALL" && state != StatePending && state != StateApproved && state != StateRejected {
		mw.WriteError(w, r, mw.InvalidQueryField("state"))
		return
	}

	bizType := strings.ToUpper(strings.TrimSpace(q.Get("biz_type")))
	if bizType != "" && !bizTypeSet[bizType] {
		mw.WriteError(w, r, mw.InvalidQueryField("biz_type"))
		return
	}

	where := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if state != "ALL" {
		where = append(where, "rt.state = ?")
		args = append(args, state)
	}
	if bizType != "" {
		where = append(where, "rt.biz_type = ?")
		args = append(args, bizType)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	ctx := r.Context()
	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_review_task rt`+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "review_list_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `
		SELECT rt.id, rt.biz_type, rt.biz_id, rt.applicant_id, rt.state,
		       rt.assignee_id, rt.decision_note, rt.decided_at, rt.created_at,
		       u.nickname, u.email
		  FROM t_review_task rt
		  LEFT JOIN t_user u ON u.id = rt.applicant_id`+whereSQL+`
		 ORDER BY rt.created_at ASC, rt.id ASC
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "review_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	tasks := make([]reviewTask, 0, page.Size)
	names := make(map[int64]string, page.Size)
	for rows.Next() {
		var (
			task      reviewTask
			assignee  sql.NullInt64
			note      sql.NullString
			decidedAt sql.NullTime
			nickname  string
			email     sql.NullString
		)
		if err := rows.Scan(&task.ID, &task.BizType, &task.BizID, &task.ApplicantID,
			&task.State, &assignee, &note, &decidedAt, &task.CreatedAt,
			&nickname, &email); err != nil {
			s.logError(ctx, "review_list_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		if assignee.Valid {
			v := assignee.Int64
			task.AssigneeID = &v
		}
		if note.Valid {
			v := note.String
			task.DecisionNote = &v
		}
		if decidedAt.Valid {
			v := decidedAt.Time.UTC()
			task.DecidedAt = &v
		}
		task.CreatedAt = task.CreatedAt.UTC()
		tasks = append(tasks, task)
		names[task.ApplicantID] = displayName(nickname, email, task.ApplicantID)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "review_list_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	items, apiErr := s.enrich(ctx, tasks, names)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, reviewListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

func displayName(nickname string, email sql.NullString, uid int64) string {
	if n := strings.TrimSpace(nickname); n != "" {
		return n
	}
	if email.Valid && strings.TrimSpace(email.String) != "" {
		return email.String
	}
	return fmt.Sprintf("用户#%d", uid)
}

// bizRow is the common shape extracted from each source table.
type bizRow struct {
	No    string
	Micro *int64
	Fen   *int64
	Text  string
	State string
}

// enrichOne loads the business row for a single queue row.
func (s *Service) enrichOne(ctx context.Context, task reviewTask) (reviewItem, *apierr.APIError) {
	var name string
	var nickname string
	var email sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT nickname, email FROM t_user WHERE id = ?`, task.ApplicantID).
		Scan(&nickname, &email)
	if err == nil {
		name = displayName(nickname, email, task.ApplicantID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.logError(ctx, "review_applicant_load_failed: "+err.Error())
		return reviewItem{}, apierr.New(apierr.CodeInternalError)
	} else {
		name = displayName("", email, task.ApplicantID)
	}
	items, apiErr := s.enrich(ctx, []reviewTask{task}, map[int64]string{task.ApplicantID: name})
	if apiErr != nil {
		return reviewItem{}, apiErr
	}
	return items[0], nil
}

// enrich batch-loads the source tables so a page of 100 queue rows is seven
// queries, not three hundred. A missing business row marks the item as
// anomalous instead of failing the request.
func (s *Service) enrich(ctx context.Context, tasks []reviewTask, names map[int64]string) ([]reviewItem, *apierr.APIError) {
	grouped := make(map[string][]int64, 5)
	for _, task := range tasks {
		grouped[task.BizType] = append(grouped[task.BizType], task.BizID)
	}

	byType := make(map[string]map[int64]bizRow, len(grouped))
	for bizType, ids := range grouped {
		rows, err := s.loadBizRows(ctx, bizType, ids)
		if err != nil {
			s.logError(ctx, "review_biz_load_failed "+bizType+": "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		byType[bizType] = rows
	}

	items := make([]reviewItem, 0, len(tasks))
	for _, task := range tasks {
		item := reviewItem{
			ID:            task.ID,
			BizType:       task.BizType,
			BizID:         task.BizID,
			ApplicantID:   task.ApplicantID,
			ApplicantName: names[task.ApplicantID],
			State:         task.State,
			AssigneeID:    task.AssigneeID,
			DecisionNote:  task.DecisionNote,
			DecidedAt:     task.DecidedAt,
			CreatedAt:     task.CreatedAt,
		}
		if item.ApplicantName == "" {
			item.ApplicantName = fmt.Sprintf("用户#%d", task.ApplicantID)
		}
		row, ok := byType[task.BizType][task.BizID]
		if !ok {
			reason := fmt.Sprintf("业务行不存在：%s#%d", tableFor(task.BizType), task.BizID)
			item.Anomaly = true
			item.AnomalyReason = &reason
			item.Summary = "关联业务行已不存在，无法展示金额与摘要"
			items = append(items, item)
			continue
		}
		item.BizNo = row.No
		item.AmountMicro = row.Micro
		item.AmountFen = row.Fen
		item.Summary = truncateRunes(row.Text, summaryMax)
		item.BusinessState = row.State
		items = append(items, item)
	}
	return items, nil
}

func tableFor(bizType string) string {
	switch bizType {
	case BizQuotaApply:
		return "t_quota_apply"
	case BizInvoiceApply:
		return "t_invoice_apply"
	case BizRefundApply:
		return "t_refund_order"
	case BizRedeemBatch:
		return "t_redeem_batch"
	case BizBalanceAdjust:
		return "t_admin_audit_log"
	default:
		return bizType
	}
}

func (s *Service) loadBizRows(ctx context.Context, bizType string, ids []int64) (map[int64]bizRow, error) {
	out := make(map[int64]bizRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	in := placeholders(len(ids))

	var query string
	switch bizType {
	case BizQuotaApply:
		query = `SELECT id, apply_no, target_quota_micro, apply_reason, state
		           FROM t_quota_apply WHERE id IN (` + in + `)`
	case BizInvoiceApply:
		query = `SELECT id, apply_no, amount_fen, title, state
		           FROM t_invoice_apply WHERE id IN (` + in + `)`
	case BizRefundApply:
		query = `SELECT id, refund_no, apply_micro, refund_fen, state
		           FROM t_refund_order WHERE id IN (` + in + `)`
	case BizRedeemBatch:
		query = `SELECT id, batch_name, face_value_fen, quantity
		           FROM t_redeem_batch WHERE id IN (` + in + `)`
	case BizBalanceAdjust:
		query = `SELECT id, action, reason, result
		           FROM t_admin_audit_log WHERE id IN (` + in + `)`
	default:
		return out, nil
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		switch bizType {
		case BizQuotaApply:
			var no, reason, state string
			var micro int64
			if err := rows.Scan(&id, &no, &micro, &reason, &state); err != nil {
				return nil, err
			}
			out[id] = bizRow{No: no, Micro: &micro, Text: reason, State: state}
		case BizInvoiceApply:
			var no, title, state string
			var fen int64
			if err := rows.Scan(&id, &no, &fen, &title, &state); err != nil {
				return nil, err
			}
			out[id] = bizRow{No: no, Fen: &fen, Text: title, State: state}
		case BizRefundApply:
			var no, state string
			var micro, fen int64
			if err := rows.Scan(&id, &no, &micro, &fen, &state); err != nil {
				return nil, err
			}
			out[id] = bizRow{No: no, Micro: &micro, Fen: &fen, Text: "退款单 " + no, State: state}
		case BizRedeemBatch:
			var name string
			var face int64
			var quantity int64
			if err := rows.Scan(&id, &name, &face, &quantity); err != nil {
				return nil, err
			}
			total := face * quantity
			out[id] = bizRow{
				No: fmt.Sprintf("BATCH#%d", id), Fen: &total, Text: name,
			}
		case BizBalanceAdjust:
			var action, result string
			var reason sql.NullString
			if err := rows.Scan(&id, &action, &reason, &result); err != nil {
				return nil, err
			}
			text := action
			if reason.Valid && reason.String != "" {
				text = action + "：" + reason.String
			}
			out[id] = bizRow{No: fmt.Sprintf("AUDIT#%d", id), Text: text, State: result}
		}
	}
	return out, rows.Err()
}
