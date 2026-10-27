// Package logq implements the account console's read-only request-log surface:
// the list (GET /api/console/logs), one request's metadata plus retry timeline
// (GET /api/console/logs/{request_id}) and the stored request body
// (GET /api/console/logs/{request_id}/body).
//
// Three rules shape every file here:
//
//   - No cross-database join. t_usage_record already carries the snapshot
//     columns the console displays (model_name_snap, channel_name_snap,
//     api_key_name_snap, group_code_snap), so the queries read one table and
//     never touch t_model / t_channel (T14 hard constraint 1).
//   - Failures stay visible. The list does not filter on result unless the
//     caller asks for a state; a failed request is a log row, not a stat row
//     (T14 hard constraint 6).
//   - Another account's request_id is indistinguishable from a missing one.
//     Every lookup carries user_id = session.uid, and both conditions answer
//     the same resource_not_found body (A8 / T14 hard constraint 9).
//
// The log database may be a distinct instance. LogDB, when non-nil, serves
// every read; otherwise the service falls back to DB so the single-database
// local deployment and the tests keep working without a second pool.
package logq

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

const (
	// onlineWindow is the console's online query window, not the retention
	// period. The metadata itself is kept 365 days; an older from is refused
	// with "use archive export" rather than returning a silently short page
	// (T14 hard constraint 2).
	onlineWindow = 365 * 24 * time.Hour

	// defaultWindow is the list default when from is omitted.
	defaultWindow = 7 * 24 * time.Hour

	// bodyWindow is the body retention period. A row older than this answers
	// 404, never an empty string (T14 hard constraint 3).
	bodyWindow = 180 * 24 * time.Hour

	// maxModelFilterRunes matches model_name_snap varchar(128).
	maxModelFilterRunes = 128
)

// Service owns the log reads. DB is the main database and the fallback when
// LogDB is nil; LogDB is the dedicated log database.
type Service struct {
	DB     *sql.DB
	LogDB  *sql.DB
	Now    clock.Clock
	Logger *obslog.Logger

	// BodyStore is the request-body seam. The default reads t_request_body,
	// which T22 creates with migration 0022; until then the table is absent
	// and every body read answers resource_not_found. Tests replace it to
	// exercise the success path without inventing production schema here.
	BodyStore bodyStore
}

// New wires the service. A nil clock falls back to the production clock; the
// default body store binds to whichever pool serves log reads.
func New(db, logDB *sql.DB, now clock.Clock, logger *obslog.Logger) *Service {
	if now == nil {
		now = clock.Real{}
	}
	s := &Service{DB: db, LogDB: logDB, Now: now, Logger: logger}
	s.BodyStore = &sqlBodyStore{DB: s.logs()}
	return s
}

// logs returns the pool every log read must use. A nil LogDB is the
// single-database deployment: the log tables live in DB.
func (s *Service) logs() *sql.DB {
	if s.LogDB != nil {
		return s.LogDB
	}
	return s.DB
}

func (s *Service) now() time.Time { return s.Now.Now().UTC() }

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "logq", Status: http.StatusInternalServerError})
	}
}

// sessionUID is the only source of the account scope. The route table already
// requires a session; a missing one here is a wiring bug answered as 401
// rather than a query with user_id = 0.
func sessionUID(r *http.Request) (int64, *apierr.APIError) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok || sess == nil {
		return 0, mw.SessionMissing()
	}
	return sess.UID, nil
}

// requestNotFound is the single body for "absent" and "owned by someone
// else". The generic apierr message is deliberate: a message naming the
// resource would let a caller distinguish the two (A8).
func requestNotFound() *apierr.APIError { return apierr.New(apierr.CodeResourceNotFound) }
