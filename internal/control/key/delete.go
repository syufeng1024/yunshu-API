package key

import (
	"net/http"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

type deleteResponse struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

// Delete implements DELETE /api/console/keys/{id} (F6-4, B-39).
//
// The row is soft-deleted with the TDS sentinel: key_hash is rewritten so the
// old plaintext can never authenticate, and the row stays because
// t_usage_record.api_key_id still references it for historical billing.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		Confirm   *bool            `json:"confirm"`
		GroupCode jsonx.RawMessage `json:"group_code"`
	}
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if len(req.GroupCode) > 0 {
		mw.WriteError(w, r, mw.InvalidField("group_code"))
		return
	}
	if req.Confirm == nil || !*req.Confirm {
		mw.WriteError(w, r, mw.InvalidField("confirm"))
		return
	}

	ctx := r.Context()
	now := s.now()
	if s.KeyAuth == nil {
		s.logError(ctx, "key_delete_k13_writer_missing")
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable))
		return
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "key_delete_begin_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	rec, apiErr := s.loadOwnedKey(ctx, tx, sess.UID, id, true)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	// LEFT(key_name, 58) + ' (已删除)' is exactly 64 characters. The TDS
	// snippet appends without truncation, which would overflow varchar(64) for
	// a 64-character name and make the delete fail in strict mode.
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_api_key
		   SET deleted_at = ?,
		       key_hash = CONCAT('#del#', id, '#', ?),
		       key_name = CONCAT(LEFT(key_name, 58), ' (已删除)'),
		       updated_at = ?
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		now, now.UnixMilli(), now, id, sess.UID); err != nil {
		s.logError(ctx, "key_delete_row_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	before := map[string]any{
		"key_name":   rec.Name,
		"key_prefix": rec.Prefix,
		"key_last4":  rec.Last4,
		"status":     rec.Status,
		"group_code": rec.GroupCode,
		"expire_at":  nullableTime(rec.ExpireAt),
	}
	after := map[string]any{"deleted_at": now}
	if err := insertKeyAudit(ctx, tx, id, "DELETE", before, after, sess.UID, auditIP(r), now); err != nil {
		s.logError(ctx, "key_delete_audit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "key_delete_commit_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	// The digest was captured before the sentinel rewrite made it unusable.
	if err := s.removeK13(ctx, rec.Hash); err != nil {
		s.logError(ctx, "key_delete_k13_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable).WithCause(err))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, deleteResponse{ID: id, Deleted: true})
}
