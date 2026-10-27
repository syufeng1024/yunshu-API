package logq

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// errBodyNotFound means "no retained body", covering three conditions that
// are deliberately indistinguishable to the caller: the request was served
// with full logging off, its body aged past 180 days, or t_request_body does
// not exist yet (T22 migration 0022, D-20).
var errBodyNotFound = errors.New("logq: no retained body")

// bodyStore is the request-body seam. The production implementation reads
// t_request_body; tests substitute a fake to exercise the success path
// without creating production schema in this segment.
type bodyStore interface {
	fetch(ctx context.Context, uid int64, requestID string) (string, error)
}

// sqlBodyStore reads t_request_body. The table is created by T22 (0022), so
// on every deployment before that migration it does not exist. That case is
// mapped to errBodyNotFound — never to a 500 and never to an empty body
// pretending to be the stored content.
type sqlBodyStore struct {
	DB *sql.DB
}

func (s *sqlBodyStore) fetch(ctx context.Context, uid int64, requestID string) (string, error) {
	// Check the four columns this reader needs, not just the table name. The
	// T09 tests create a throwaway t_request_body fixture with a different
	// shape, and `go test ./...` runs packages concurrently; a name-only
	// check would make that race answer 500. A table that cannot serve the
	// query is reported as "no retained body", which is what it is.
	var columns int
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT column_name) FROM information_schema.columns
		 WHERE table_schema = DATABASE() AND table_name = 't_request_body'
		   AND column_name IN ('request_id', 'user_id', 'ciphertext', 'body_key_id')`).Scan(&columns); err != nil {
		return "", err
	}
	if columns < 4 {
		return "", errBodyNotFound
	}

	var (
		ciphertext []byte
		bodyKeyID  sql.NullString
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT ciphertext, body_key_id
		  FROM t_request_body
		 WHERE request_id = ? AND user_id = ?
		 LIMIT 1`, requestID, uid).Scan(&ciphertext, &bodyKeyID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errBodyNotFound
	}
	if err != nil {
		return "", err
	}
	// An empty ciphertext is not a readable body. Reporting it as success
	// would be the empty-string pretence the card forbids, so it is mapped to
	// the same 404 as a missing row.
	if len(ciphertext) == 0 {
		return "", errBodyNotFound
	}
	// T22 owns decryption (body_key_id selects the key). Until then no row can
	// exist, and returning the stored bytes is the contract this seam will
	// keep once decrypt is wired inside the store.
	return string(ciphertext), nil
}

type bodyResponse struct {
	RequestID string    `json:"request_id"`
	ModelName string    `json:"model_name"`
	CreatedAt time.Time `json:"created_at"`
	Body      string    `json:"body"`
	Redacted  bool      `json:"redacted"`
}

// Body implements GET /api/console/logs/{request_id}/body.
//
// Ownership is checked first, so another account's request_id answers the
// same generic 404 as a nonexistent one. Only after ownership is proven does
// the endpoint explain that a body is unavailable (retention or opt-in).
func (s *Service) Body(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	requestID := r.PathValue("request_id")
	ctx := r.Context()

	req, found, apiErr := s.loadRequest(ctx, uid, requestID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if !found {
		mw.WriteError(w, r, requestNotFound())
		return
	}

	now := s.now()
	if req.CreatedAt.Before(now.Add(-bodyWindow)) {
		mw.WriteError(w, r, bodyNotFound())
		return
	}
	// full_log_saved is written by the relay at request time. A zero here
	// means the body was never stored, so there is nothing to look up.
	if !req.FullLogSaved {
		mw.WriteError(w, r, bodyNotFound())
		return
	}

	store := s.BodyStore
	if store == nil {
		store = &sqlBodyStore{DB: s.logs()}
	}
	body, err := store.fetch(ctx, uid, requestID)
	if errors.Is(err, errBodyNotFound) {
		mw.WriteError(w, r, bodyNotFound())
		return
	}
	if err != nil {
		s.logError(ctx, "logq_body_fetch_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	safe, changed := redactBody(body)
	_ = mw.WriteJSON(w, http.StatusOK, bodyResponse{
		RequestID: req.RequestID,
		ModelName: req.ModelName,
		CreatedAt: req.CreatedAt,
		Body:      safe,
		Redacted:  changed,
	})
}

// bodyNotFound is resource_not_found with a body-specific explanation. It is
// never used for an ownership failure: that path is requestNotFound(), which
// must stay byte-identical to the absent-row case (A8).
func bodyNotFound() *apierr.APIError {
	e := apierr.New(apierr.CodeResourceNotFound)
	e.Message = "正文未开启或不在此保留期内"
	return e.WithHint("完整日志需在账号设置中开启；正文仅保留 180 天")
}
