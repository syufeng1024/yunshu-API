// Package auditq implements the operator-side audit reader:
// GET /api/admin/audit-logs over t_admin_audit_log.
//
// The table is append-only. 0024 adds the two keys every query window needs:
//
//	idx_aal_created        (created_at, id)
//	idx_aal_action_created (action, created_at, id)
//
// together with the pre-existing operator/module keys. The window always
// carries created_at, so a filter on action or result alone is a bounded range
// scan with a residual predicate, never a full table scan. Ordering is
// created_at DESC, id DESC; see list.go.
package auditq

import (
	"context"
	"database/sql"
	"net/http"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// Service owns the audit-log reads.
type Service struct {
	DB     *sql.DB
	Now    clock.Clock
	Logger *obslog.Logger
}

// New wires the service. A nil clock falls back to the production clock.
func New(db *sql.DB, now clock.Clock, logger *obslog.Logger) *Service {
	if now == nil {
		now = clock.Real{}
	}
	return &Service{DB: db, Now: now, Logger: logger}
}

func (s *Service) now() clock.Clock {
	if s.Now != nil {
		return s.Now
	}
	return clock.Real{}
}

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "auditq", Status: http.StatusInternalServerError})
	}
}
