// Package control assembles the controld HTTP surface: /api/v1/auth/*,
// /api/console/* and /api/admin/*.
//
// Every route is registered through one helper that declares its permission.
// A handler cannot opt out of authorization by forgetting a menu entry: the
// permission is part of the route table in this file (T11 hard constraint 1).
package control

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/apikey"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/auth"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// Deps is the control plane's dependency set. controld owns the general main
// database and Redis pools (TDS §1).
type Deps struct {
	DB       *sql.DB
	Redis    *redisx.Client
	Sessions *mw.SessionManager
	Audit    *audit.Writer
	Logger   *obslog.Logger
	Now      clock.Clock
}

// Server holds the route table and its dependencies.
type Server struct {
	d    Deps
	auth *auth.Service
}

// New validates the wiring and builds the server.
func New(d Deps) (*Server, error) {
	if d.DB == nil || d.Redis == nil || d.Sessions == nil {
		return nil, errors.New("control: server requires mysql, redis and a session manager")
	}
	if d.Audit == nil {
		d.Audit = &audit.Writer{DB: d.DB, Now: d.Now}
	}
	if d.Now == nil {
		d.Now = clock.Real{}
	}
	return &Server{
		d: d,
		auth: &auth.Service{
			DB:       d.DB,
			Sessions: d.Sessions,
			Audit:    d.Audit,
			Logger:   d.Logger,
			Now:      d.Now,
		},
	}, nil
}

// Register installs every control-plane route. Each entry names its permission
// in one place; adding a route without a permission is a compile-time omission
// here, not a runtime surprise.
func (s *Server) Register(mux *http.ServeMux) {
	// Public: unauthenticated by design.
	mux.Handle("POST /api/v1/auth/register", s.public(s.auth.Register))
	mux.Handle("POST /api/v1/auth/login", s.public(s.auth.Login))

	// Authenticated, no capability required.
	mux.Handle("GET /api/v1/auth/me", s.authed("", s.auth.Me))
	mux.Handle("POST /api/v1/auth/logout", s.authed("", s.auth.Logout))

	// Console: model.read for the catalog, key.write for key creation.
	mux.Handle("GET /api/console/models", s.authed(mw.PermModelRead, s.handleConsoleModels))
	mux.Handle("POST /api/console/keys", s.authed(mw.PermKeyWrite, s.handleKeyCreate))

	// Admin: the first segment wires one read-only diagnostic. The business
	// admin endpoints arrive with T15 and inherit the same admin.all gate.
	mux.Handle("GET /api/admin/whoami", s.authed(mw.PermAdminAll, s.handleAdminWhoami))

	// Unknown /api paths return the shared error envelope instead of net/http's
	// plain-text 404.
	mux.Handle("/api/", s.public(func(w http.ResponseWriter, r *http.Request) {
		mw.WriteError(w, r, apierr.New(apierr.CodeUnknownEndpoint))
	}))
}

// public wraps a handler with panic recovery only.
func (s *Server) public(h http.HandlerFunc) http.Handler {
	return mw.Recover(s.d.Logger)(h)
}

// authed builds Recover → Session → Require(perm) → handler. An empty perm is
// the "logged in, no capability" case (me/logout).
func (s *Server) authed(perm mw.Permission, h http.HandlerFunc) http.Handler {
	var chain http.Handler = h
	if perm != "" {
		chain = mw.Require(perm)(chain)
	}
	chain = s.d.Sessions.Middleware()(chain)
	return mw.Recover(s.d.Logger)(chain)
}

// ---------------------------------------------------------------------------
// GET /api/console/models — model.read, read-only, LISTED + DOMESTIC only.
// ---------------------------------------------------------------------------

type modelItem struct {
	ID            int64  `json:"id"`
	ModelName     string `json:"model_name"`
	DisplayName   string `json:"display_name"`
	ModelType     string `json:"model_type"`
	ContextLength int64  `json:"context_length"`
	ListingState  string `json:"listing_state"`
}

type modelListResponse struct {
	Items    []modelItem `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int64       `json:"total"`
}

func (s *Server) handleConsoleModels(w http.ResponseWriter, r *http.Request) {
	page, apiErr := mw.ParsePage(r, "model_name", "published_at", "recommend_weight")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	orderBy := "model_name"
	switch page.Sort {
	case "published_at":
		orderBy = "published_at"
	case "recommend_weight":
		orderBy = "recommend_weight"
	}
	direction := "ASC"
	if page.Order == "desc" {
		direction = "DESC"
	}

	var total int64
	if err := s.d.DB.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM t_model
		 WHERE deleted_at IS NULL AND listing_state = 'LISTED' AND region = 'DOMESTIC'`,
	).Scan(&total); err != nil {
		s.logError(r.Context(), "model_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	rows, err := s.d.DB.QueryContext(r.Context(), `
		SELECT id, model_name, display_name, model_type, context_length, listing_state
		  FROM t_model
		 WHERE deleted_at IS NULL AND listing_state = 'LISTED' AND region = 'DOMESTIC'
		 ORDER BY `+orderBy+` `+direction+`
		 LIMIT ? OFFSET ?`, page.Size, page.OffsetInt64())
	if err != nil {
		s.logError(r.Context(), "model_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]modelItem, 0, page.Size)
	for rows.Next() {
		var m modelItem
		if err := rows.Scan(&m.ID, &m.ModelName, &m.DisplayName, &m.ModelType,
			&m.ContextLength, &m.ListingState); err != nil {
			s.logError(r.Context(), "model_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		s.logError(r.Context(), "model_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, modelListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// ---------------------------------------------------------------------------
// POST /api/console/keys — key.write, minimal creation.
//
// The full T12 surface (batch count, IP rules, rotation, update) is out of
// scope. This endpoint exists because key.write must protect a real route, and
// because the first segment needs a way to create a key without seedctl.
// ---------------------------------------------------------------------------

type createKeyRequest struct {
	Name      string  `json:"name"`
	ModelIDs  []int64 `json:"model_ids"`
	ExpireAt  *string `json:"expire_at"`
	GroupCode *string `json:"group_code"` // must be absent: the user cannot pick a group
}

type createKeyResponse struct {
	ID             int64   `json:"id"`
	KeyName        string  `json:"key_name"`
	Key            string  `json:"key"`
	KeyPrefix      string  `json:"key_prefix"`
	KeyLast4       string  `json:"key_last4"`
	ModelScopeMode string  `json:"model_scope_mode"`
	ModelIDs       []int64 `json:"model_ids"`
	GroupCode      string  `json:"group_code"`
	Warning        string  `json:"warning"`
}

func (s *Server) handleKeyCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	var req createKeyRequest
	if _, apiErr := decodeBody(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if req.GroupCode != nil {
		// PRD F7-4 / T12 hard constraint 3. Rejecting is the only safe answer:
		// ignoring the field would make the caller think the group changed.
		mw.WriteError(w, r, mw.InvalidField("group_code"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 64 {
		mw.WriteError(w, r, mw.InvalidField("name"))
		return
	}
	modelIDs, apiErr := s.validateModelIDs(r.Context(), req.ModelIDs)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	expireAt, apiErr := parseExpireAt(req.ExpireAt, s.d.Now.Now().UTC())
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	key, hash, err := apikey.Generate()
	if err != nil {
		s.logError(r.Context(), "key_generate_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	hash16, err := apikey.HashPrefix16(hash)
	if err != nil {
		s.logError(r.Context(), "key_hash_prefix_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	const groupCode = "standard"
	now := s.d.Now.Now().UTC()

	kid, apiErr := s.insertKey(r.Context(), r, sess, name, hash, key, modelIDs, groupCode, expireAt, now)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	// Write-through to K13. A failure leaves the key unusable until the next
	// refresh; it is reported rather than hidden, and the key row stays because
	// the audit trail must match what the caller was told.
	if err := s.writeK13(r.Context(), hash16, sess.UID, kid, name, groupCode, expireAt); err != nil {
		s.logError(r.Context(), "key_k13_write_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable))
		return
	}

	_ = mw.WriteJSON(w, http.StatusCreated, createKeyResponse{
		ID:             kid,
		KeyName:        name,
		Key:            key,
		KeyPrefix:      apikey.DisplayPrefix(key),
		KeyLast4:       apikey.Last4(key),
		ModelScopeMode: "SELECTED",
		ModelIDs:       modelIDs,
		GroupCode:      groupCode,
		Warning:        "完整密钥仅本次返回，关闭后无法再次查看",
	})
}

func (s *Server) validateModelIDs(ctx context.Context, raw []int64) ([]int64, *apierr.APIError) {
	if len(raw) == 0 {
		return nil, mw.InvalidField("model_ids")
	}
	if len(raw) > 100 {
		return nil, mw.InvalidField("model_ids")
	}
	seen := make(map[int64]bool, len(raw))
	out := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, mw.InvalidField("model_ids")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	rows, err := s.d.DB.QueryContext(ctx,
		`SELECT id FROM t_model WHERE deleted_at IS NULL AND id IN (`+placeholders(len(out))+`)`,
		int64Args(out)...)
	if err != nil {
		s.logError(ctx, "model_scope_lookup_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		found++
	}
	if err := rows.Err(); err != nil || found != len(out) {
		return nil, mw.InvalidField("model_ids")
	}
	return out, nil
}

func (s *Server) insertKey(ctx context.Context, r *http.Request, sess *mw.Session,
	name, hash, plaintext string, modelIDs []int64, groupCode string, expireAt *time.Time, now time.Time) (int64, *apierr.APIError) {
	tx, err := s.d.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "key_begin_tx_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var expire any
	if expireAt != nil {
		expire = expireAt.UTC()
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_api_key
			(user_id, key_name, key_prefix, key_hash, key_hash_version, key_last4,
			 status, model_scope_mode, ip_whitelist_mode, group_code, expire_at, created_at, updated_at)
		VALUES (?,?,?,?,1,?,'ENABLED','SELECTED','OFF',?,?,?,?)`,
		sess.UID, name, apikey.DisplayPrefix(plaintext), hash, apikey.Last4(plaintext),
		groupCode, expire, now, now)
	if err != nil {
		s.logError(ctx, "key_insert_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	kid, err := res.LastInsertId()
	if err != nil {
		return 0, apierr.New(apierr.CodeInternalError)
	}
	for _, modelID := range modelIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO t_api_key_model (api_key_id, model_id, created_at) VALUES (?,?,?)`,
			kid, modelID, now); err != nil {
			s.logError(ctx, "key_model_insert_failed: "+err.Error())
			return 0, apierr.New(apierr.CodeInternalError)
		}
	}

	after, err := jsonx.Marshal(map[string]any{
		"key_name":         name,
		"key_prefix":       apikey.DisplayPrefix(plaintext),
		"key_last4":        apikey.Last4(plaintext),
		"model_scope_mode": "SELECTED",
		"model_ids":        modelIDs,
		"group_code":       groupCode,
		"expire_at":        expireAt,
	})
	if err != nil {
		return 0, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO t_api_key_audit
			(api_key_id, action, before_json, after_json, operator_id, operator_ip, created_at)
		VALUES (?, 'CREATE', NULL, ?, ?, ?, ?)`,
		kid, string(after), sess.UID, audit.IPBytes(r), now); err != nil {
		s.logError(ctx, "key_audit_insert_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	if err := tx.Commit(); err != nil {
		s.logError(ctx, "key_commit_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	return kid, nil
}

func (s *Server) writeK13(ctx context.Context, hash16 string, uid, kid int64, name, groupCode string, expireAt *time.Time) error {
	var gp int64 = 10000
	if err := s.d.DB.QueryRowContext(ctx,
		`SELECT ratio_bp FROM t_price_group WHERE code = ?`, groupCode).Scan(&gp); err != nil {
		// A missing group is a configuration fault. Do not silently write 1.0.
		return err
	}
	var exp int64
	if expireAt != nil {
		exp = expireAt.UTC().UnixMilli()
	}
	return redisx.NewKeyAuthCache(s.d.Redis).Upsert(ctx, hash16, redisx.KeyAuthRecord{
		UID: uid,
		KID: kid,
		ST:  "ENABLED",
		IPM: 0,
		EXP: exp,
		NT:  "",
		KN:  name,
		GRP: groupCode,
		GP:  gp,
	})
}

// ---------------------------------------------------------------------------
// GET /api/admin/whoami — admin.all, read-only diagnostic.
// ---------------------------------------------------------------------------

type whoamiResponse struct {
	UID         string          `json:"uid"`
	Role        string          `json:"role"`
	Permissions []mw.Permission `json:"permissions"`
}

func (s *Server) handleAdminWhoami(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, whoamiResponse{
		UID:         strconv.FormatInt(sess.UID, 10),
		Role:        sess.Role,
		Permissions: mw.PermissionsFor(sess.Role),
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (s *Server) logError(ctx context.Context, msg string) {
	if s.d.Logger != nil {
		s.d.Logger.Error(ctx, msg, obslog.Fields{Stage: "control", Status: http.StatusInternalServerError})
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) ([]byte, *apierr.APIError) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	payload, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(payload))) == 0 {
		return nil, mw.InvalidField("body")
	}
	if err := jsonx.Unmarshal(payload, dst); err != nil {
		return nil, mw.InvalidField("body")
	}
	return payload, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func parseExpireAt(raw *string, now time.Time) (*time.Time, *apierr.APIError) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
	if err != nil || !t.After(now) {
		return nil, mw.InvalidField("expire_at")
	}
	return &t, nil
}
