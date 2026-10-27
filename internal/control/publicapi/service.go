// Package publicapi implements the anonymous /api/public/* surface consumed by
// the marketing site (web/src/api/public.ts).
//
// These routes are read-only, need no session and carry no permission: they are
// registered through control.Server.public (panic recovery only), never
// through the Session → CSRF → Require chain.
//
// The JSON key names are the frontend contract, not the column names:
// PublicModel.context_window maps t_model.context_length, and
// PublicAnnouncement.body maps t_announcement.content. Renaming either breaks
// the site at runtime with no compile error, so the handler tests pin the exact
// key set.
package publicapi

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

// publicCache is the short shared-cache window the site accepts. Catalogue and
// announcement changes are operator-driven and infrequent; one minute keeps the
// pages responsive without making every visitor hit MySQL.
const publicCache = "public, max-age=60"

// Service owns the public read queries.
type Service struct {
	DB     *sql.DB
	Now    clock.Clock
	Logger *obslog.Logger
}

// New wires the public service. A nil clock falls back to the real one so the
// effective-window queries have a single source of "now".
func New(db *sql.DB, now clock.Clock, logger *obslog.Logger) *Service {
	if now == nil {
		now = clock.Real{}
	}
	return &Service{DB: db, Now: now, Logger: logger}
}

func (s *Service) now() time.Time { return s.Now.Now().UTC() }

func (s *Service) logError(ctx context.Context, msg string) {
	if s.Logger != nil {
		s.Logger.Error(ctx, msg, obslog.Fields{Stage: "control", Status: http.StatusInternalServerError})
	}
}

// write emits a 200 with the cache header. Error responses deliberately do not
// take this path: a 500 must never be cached for a minute, or a transient MySQL
// failure would keep the site empty after the database recovers.
func (s *Service) write(w http.ResponseWriter, body any) {
	w.Header().Set("Cache-Control", publicCache)
	_ = mw.WriteJSON(w, http.StatusOK, body)
}

// fail logs the SQL error and answers the shared 500 envelope. No public-specific
// error code is introduced; the site only needs "retry later".
func (s *Service) fail(w http.ResponseWriter, r *http.Request, msg string) {
	s.logError(r.Context(), msg)
	mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
}
