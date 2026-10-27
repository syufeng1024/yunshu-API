package publicapi

import (
	"net/http"
	"strconv"
	"time"
)

// announcementLimit bounds the rail on the landing page. The site does not
// paginate announcements, so a stale backlog must not grow the payload.
const announcementLimit = 20

// publicAnnouncement mirrors web/src/api/public.ts PublicAnnouncement. Three
// columns change name on the way out: content→body, ann_type→category,
// effective_from→effective_at. published_at is t_announcement.created_at: the
// site means "when did this notice appear", not "when does it take effect"
// (the latter is effective_at).
type publicAnnouncement struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Category    string    `json:"category"`
	EffectiveAt time.Time `json:"effective_at"`
	PublishedAt time.Time `json:"published_at"`
}

type announcementsResponse struct {
	Data []publicAnnouncement `json:"data"`
}

// Announcements implements GET /api/public/announcements.
//
// Only PUBLISHED rows inside their effective window are visible. SCHEDULED
// rows (F17-2) are not: their effective_from is in the future, so the window
// check already excludes them; the explicit state check also keeps DRAFT and
// OFFLINE rows out even if an operator scheduled one in the past.
func (s *Service) Announcements(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT id, title, content, ann_type, effective_from, created_at
		  FROM t_announcement
		 WHERE state = 'PUBLISHED'
		   AND effective_from <= ?
		   AND (effective_to IS NULL OR effective_to > ?)
		   AND deleted_at IS NULL
		 ORDER BY effective_from DESC, id DESC
		 LIMIT ?`, now, now, announcementLimit)
	if err != nil {
		s.fail(w, r, "public_announcements_failed: "+err.Error())
		return
	}
	defer rows.Close()

	data := make([]publicAnnouncement, 0, announcementLimit)
	for rows.Next() {
		var (
			a           publicAnnouncement
			id          int64
			annType     string
			effectiveAt time.Time
			createdAt   time.Time
		)
		if err := rows.Scan(&id, &a.Title, &a.Body, &annType, &effectiveAt, &createdAt); err != nil {
			s.fail(w, r, "public_announcements_scan_failed: "+err.Error())
			return
		}
		a.ID = strconv.FormatInt(id, 10)
		a.Category = publicCategory(annType)
		a.EffectiveAt = effectiveAt.UTC()
		a.PublishedAt = createdAt.UTC()
		data = append(data, a)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, "public_announcements_rows_failed: "+err.Error())
		return
	}
	s.write(w, announcementsResponse{Data: data})
}

// publicCategory maps t_announcement.ann_type onto the site's
// AnnouncementCategory union ('PRICE' | 'RATE_LIMIT' | 'GENERAL').
//
// The backend has no RATE_LIMIT type in V1.0; a rate-limit notice is written as
// NOTICE today. Emitting the raw ann_type would leave every badge unlabelled
// because the frontend record only has labels for the three public names.
func publicCategory(annType string) string {
	switch annType {
	case "PRICE_CHANGE":
		return "PRICE"
	default:
		return "GENERAL"
	}
}
