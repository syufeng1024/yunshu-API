package key

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertKeyAudit writes one t_api_key_audit row. before/after are marshalled
// here so every call site records the same shape; key plaintext is never part
// of either map (the audit table has no column for it, and callers pass only
// prefix/last4).
func insertKeyAudit(ctx context.Context, q execer, keyID int64, action string, before, after any, operatorID int64, ip []byte, now time.Time) error {
	var beforeRaw, afterRaw any
	if before != nil {
		raw, err := jsonx.Marshal(before)
		if err != nil {
			return err
		}
		beforeRaw = string(raw)
	}
	if after != nil {
		raw, err := jsonx.Marshal(after)
		if err != nil {
			return err
		}
		afterRaw = string(raw)
	}
	var ipArg any
	if len(ip) > 0 {
		ipArg = ip
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO t_api_key_audit
			(api_key_id, action, before_json, after_json, operator_id, operator_ip, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		keyID, action, beforeRaw, afterRaw, operatorID, ipArg, now)
	return err
}

type auditItem struct {
	Action     string           `json:"action"`
	Before     jsonx.RawMessage `json:"before"`
	After      jsonx.RawMessage `json:"after"`
	OperatorID string           `json:"operator_id"`
	CreatedAt  time.Time        `json:"created_at"`
}

type auditResponse struct {
	Items    []auditItem `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int64       `json:"total"`
}

// ListAudit implements GET /api/console/keys/{id}/audit (F7-3).
func (s *Service) ListAudit(w http.ResponseWriter, r *http.Request) {
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
	if _, apiErr := s.loadOwnedKey(r.Context(), s.DB, sess.UID, id, false); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	page, apiErr := mw.ParsePage(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var total int64
	if err := s.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM t_api_key_audit WHERE api_key_id = ?`, id).Scan(&total); err != nil {
		s.logError(r.Context(), "key_audit_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT action, before_json, after_json, operator_id, created_at
		  FROM t_api_key_audit
		 WHERE api_key_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ? OFFSET ?`, id, page.Size, page.OffsetInt64())
	if err != nil {
		s.logError(r.Context(), "key_audit_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]auditItem, 0, page.Size)
	for rows.Next() {
		var (
			it       auditItem
			operator int64
			before   []byte
			after    []byte
		)
		if err := rows.Scan(&it.Action, &before, &after, &operator, &it.CreatedAt); err != nil {
			s.logError(r.Context(), "key_audit_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		if len(before) > 0 {
			it.Before = before
		}
		if len(after) > 0 {
			it.After = after
		}
		it.OperatorID = strconv.FormatInt(operator, 10)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "key_audit_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, auditResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// auditIP is exported for create/update/rotate call sites so the request peer
// address is captured the same way everywhere.
func auditIP(r *http.Request) []byte { return audit.IPBytes(r) }
