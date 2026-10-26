package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
)

type loginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
	Code       string `json:"code"`
}

type loginResponse struct {
	User userView `json:"user"`
}

// Login implements POST /api/v1/auth/login with email/phone + password.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if _, apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if strings.TrimSpace(req.Code) != "" {
		s.warn(r.Context(), "login_code_rejected: verification is not enabled in segment 1")
		mw.WriteError(w, r, mw.InvalidField("code"))
		return
	}
	ident, err := NormalizeIdentifier(req.Identifier)
	if err != nil || strings.TrimSpace(req.Password) == "" {
		s.auditLogin(r.Context(), r, 0, "ANONYMOUS", "-", "REJECTED_BY_GUARD")
		mw.WriteError(w, r, apierr.New(apierr.CodeInvalidAPIKey))
		return
	}

	row, findErr := s.findUser(r.Context(), ident)
	if findErr != nil {
		if errors.Is(findErr, sql.ErrNoRows) {
			// Equalize the timing with a real bcrypt comparison so the response
			// time does not reveal whether the account exists.
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(req.Password))
			s.auditLogin(r.Context(), r, 0, "ANONYMOUS", "-", "REJECTED_BY_GUARD")
			mw.WriteError(w, r, apierr.New(apierr.CodeInvalidAPIKey))
			return
		}
		s.errorLog(r.Context(), "login_lookup_failed: "+findErr.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable))
		return
	}

	if row.algo != "bcrypt" {
		// argon2id rows are a planned upgrade, not implemented in segment 1.
		// Failing closed beats treating an argon2id hash as a bcrypt mismatch,
		// which would lock the user out with a misleading "invalid key".
		s.errorLog(r.Context(), "login_unsupported_password_algo")
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(row.hash), []byte(req.Password)); err != nil {
		s.auditLogin(r.Context(), r, row.uid, row.role, strconv.FormatInt(row.uid, 10), "REJECTED_BY_GUARD")
		mw.WriteError(w, r, apierr.New(apierr.CodeInvalidAPIKey))
		return
	}

	switch row.status {
	case "NORMAL":
	case "DISABLED":
		s.auditLogin(r.Context(), r, row.uid, row.role, strconv.FormatInt(row.uid, 10), "REJECTED_BY_GUARD")
		mw.WriteError(w, r, apierr.New(apierr.CodeAccountDisabled))
		return
	case "FROZEN_OVERDRAFT":
		s.auditLogin(r.Context(), r, row.uid, row.role, strconv.FormatInt(row.uid, 10), "REJECTED_BY_GUARD")
		mw.WriteError(w, r, apierr.New(apierr.CodeAccountFrozenDueToOverdraft))
		return
	default:
		s.auditLogin(r.Context(), r, row.uid, row.role, strconv.FormatInt(row.uid, 10), "REJECTED_BY_GUARD")
		mw.WriteError(w, r, apierr.New(apierr.CodeAccountDisabled))
		return
	}

	_, cookie, err := s.Sessions.Issue(r.Context(), row.uid)
	if err != nil {
		s.errorLog(r.Context(), "login_session_issue_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable))
		return
	}
	s.Sessions.SetCookie(w, cookie)
	s.auditLogin(r.Context(), r, row.uid, row.role, strconv.FormatInt(row.uid, 10), "OK")
	_ = mw.WriteJSON(w, http.StatusOK, loginResponse{
		User: view(row.uid, row.email, row.phone, row.nickname, row.role, row.status),
	})
}

// Me implements GET /api/v1/auth/me.
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, view(
		sess.UID, sess.Email, sess.Phone, sess.Nickname, sess.Role, sess.Status))
}

// Logout implements POST /api/v1/auth/logout. Deleting K20 makes the old sid
// unusable immediately; the browser cookie is cleared as a convenience.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if ok {
		s.Sessions.Revoke(r.Context(), sess.SID)
	}
	s.Sessions.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type userRow struct {
	uid      int64
	hash     string
	algo     string
	status   string
	role     string
	email    string
	phone    string
	nickname string
}

func (s *Service) findUser(ctx context.Context, ident Identifier) (userRow, error) {
	column := "email"
	if ident.Kind == KindPhone {
		column = "phone"
	}
	q := `SELECT id, password_hash, password_algo, status, role,
		COALESCE(email, ''), COALESCE(phone, ''), nickname
		FROM t_user
		WHERE deleted_at IS NULL AND ` + column + ` = ?`
	var row userRow
	err := s.DB.QueryRowContext(ctx, q, ident.Value).Scan(
		&row.uid, &row.hash, &row.algo, &row.status, &row.role,
		&row.email, &row.phone, &row.nickname,
	)
	if err != nil {
		return userRow{}, err
	}
	return row, nil
}

// auditLogin is best-effort: an audit outage must not stop a legitimate login,
// but it is logged so the gap is visible. State-changing endpoints
// (registration, key creation) write their audit row in the same transaction.
func (s *Service) auditLogin(ctx context.Context, r *http.Request, uid int64, role, target, result string) {
	if s.Audit == nil {
		return
	}
	if err := s.Audit.Insert(ctx, s.DB, audit.Entry{
		OperatorID:   uid,
		OperatorRole: role,
		OperatorIP:   audit.IPBytes(r),
		Module:       "USER",
		Action:       "LOGIN",
		TargetType:   "t_user",
		TargetID:     target,
		Result:       result,
	}); err != nil {
		s.warn(ctx, "login_audit_failed: "+err.Error())
	}
}

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// dummyPasswordHash is a cost-12 hash used only to equalize the not-found path.
func dummyPasswordHash() []byte {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("yunshu-dummy-password"), bcryptCost)
	})
	return dummyHash
}
