package key

import (
	"context"
	"database/sql"
	"fmt"

	"yunshu.dev/core/internal/apikey"
	"yunshu.dev/core/internal/redisx"
)

// K13 write-through (TDS §2.1 / T12 deliverable 13).
//
// gatewayd has no primary database, so a key that is cached in MySQL but not
// in K13 is simply unusable. Every state-changing path in this package calls
// one of the functions below after its transaction commits. A failure is
// logged and surfaced as 503: the database state is authoritative and the
// one-minute Refresher rebuild will repair K13, but the caller must not be told
// the key is ready when the gateway cannot see it.

func expireMillis(t sql.NullTime) int64 {
	if !t.Valid {
		return 0
	}
	return t.Time.UTC().UnixMilli()
}

// pushK13 upserts one key. It reads the group ratio from the row's join and
// the IP-rule version (max rule id) from the current database state.
func (s *Service) pushK13(ctx context.Context, rec keyRecord) error {
	return s.pushK13WithIPM(ctx, rec, s.maxIPRuleID(ctx, rec.ID))
}

// pushK13WithIPM is pushK13 with an explicit IP-rule version. The update path
// uses it when the routing snapshot could not be republished: writing the new
// rule version while the snapshot still carries the old one would make P06
// deny every request with a version mismatch.
func (s *Service) pushK13WithIPM(ctx context.Context, rec keyRecord, ipm int64) error {
	if s.KeyAuth == nil {
		return fmt.Errorf("key: K13 writer is not configured")
	}
	hash16, err := apikey.HashPrefix16(rec.Hash)
	if err != nil {
		return fmt.Errorf("key: key %d has an unusable digest: %w", rec.ID, err)
	}
	if !rec.GroupRatioBP.Valid {
		return fmt.Errorf("key: key %d references unknown price group %q", rec.ID, rec.GroupCode)
	}
	return s.KeyAuth.Upsert(ctx, hash16, redisx.KeyAuthRecord{
		UID: rec.UserID,
		KID: rec.ID,
		ST:  rec.Status,
		IPM: ipm,
		EXP: expireMillis(rec.ExpireAt),
		NT:  "",
		KN:  rec.Name,
		GRP: rec.GroupCode,
		GP:  rec.GroupRatioBP.Int64,
	})
}

// removeK13 deletes a revoked key. The digest must be captured before a soft
// delete rewrites key_hash to the sentinel value.
func (s *Service) removeK13(ctx context.Context, hash string) error {
	if s.KeyAuth == nil {
		return fmt.Errorf("key: K13 writer is not configured")
	}
	hash16, err := apikey.HashPrefix16(hash)
	if err != nil {
		return fmt.Errorf("key: digest is not usable for K13 delete: %w", err)
	}
	return s.KeyAuth.Delete(ctx, hash16)
}

// groupRatio reads the ratio for a group code. The caller has already
// validated the group exists; a miss here is a configuration fault.
func groupRatio(ctx context.Context, q rowQueryer, code string) (sql.NullInt64, error) {
	var ratio int64
	err := q.QueryRowContext(ctx,
		`SELECT ratio_bp FROM t_price_group WHERE code = ? AND enabled = 1`, code).Scan(&ratio)
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: ratio, Valid: true}, nil
}
