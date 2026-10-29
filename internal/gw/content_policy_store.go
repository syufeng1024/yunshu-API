package gw

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

// ContentPolicyHitWriter is the t_content_policy_hit writer. It uses the
// restricted gateway write channel the caller passes in (cmd/gatewayd's gwDB);
// the package itself never opens a pool.
//
// Only the excerpt is persisted, never the message text. request_id and
// model_name_snap are rune-truncated to their column widths because a client
// may put more than 36 characters in X-Request-Id, and MySQL strict mode turns
// an overlong value into an insert error that would lose the evidence row.
type ContentPolicyHitWriter struct {
	DB *sql.DB
}

// Record implements PolicyHitStore. A duplicate request_id means the same
// request already produced a row (the unique key uk_cph_request); the first row
// is the evidence and the duplicate is not an error.
func (w ContentPolicyHitWriter) Record(ctx context.Context, hit PolicyHit) error {
	if w.DB == nil {
		return errors.New("gw: content policy hit writer has no database")
	}
	createdAt := hit.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	offset := hit.SnippetOffset
	if offset < 0 {
		offset = 0
	}
	var clientIP any
	if hit.ClientIP.IsValid() {
		clientIP = hit.ClientIP.AsSlice()
	}
	_, err := w.DB.ExecContext(ctx, `
		INSERT INTO t_content_policy_hit
			(request_id, user_id, api_key_id, model_name_snap, rule_id, rule_version,
			 category, severity, matched_excerpt, snippet_sha256, snippet_offset,
			 client_ip, action, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		truncatePolicyRunes(hit.RequestID, 36),
		hit.UserID,
		hit.APIKeyID,
		truncatePolicyRunes(hit.ModelName, 128),
		truncatePolicyRunes(hit.RuleID, 64),
		truncatePolicyRunes(hit.RuleVersion, 32),
		truncatePolicyRunes(hit.Category, 32),
		truncatePolicyRunes(hit.Severity, 8),
		truncatePolicyRunes(hit.MatchedExcerpt, 255),
		hit.SnippetSHA256,
		offset,
		clientIP,
		truncatePolicyRunes(hit.Action, 16),
		createdAt.UTC(),
	)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1062 {
			return nil
		}
		return err
	}
	return nil
}
