package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
)

// bcryptCost is the TDS-mandated cost. 12 is ~100ms on current hardware; the
// login endpoint is the only place that pays it.
const bcryptCost = 12

// registerRequest is the first-segment registration body. `code` is accepted
// in the struct only to reject it explicitly: silently ignoring a verification
// code would let a caller believe the mailbox was proven when it was not.
type registerRequest struct {
	Identifier       string `json:"identifier"`
	Password         string `json:"password"`
	Code             string `json:"code"`
	Nickname         string `json:"nickname"`
	AgreementVersion string `json:"agreement_version"`
}

type registerResponse struct {
	User userView `json:"user"`
}

// Register implements POST /api/v1/auth/register.
func (s *Service) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if _, apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if strings.TrimSpace(req.Code) != "" {
		// Verification codes are a second-segment deliverable. Rejecting the
		// field is deliberate: accepting it would imply verification happened.
		s.warn(r.Context(), "register_code_rejected: verification is not enabled in segment 1")
		mw.WriteError(w, r, mw.InvalidField("code"))
		return
	}
	ident, err := NormalizeIdentifier(req.Identifier)
	if err != nil {
		mw.WriteError(w, r, mw.InvalidField("identifier"))
		return
	}
	if err := validatePassword(req.Password); err != nil {
		mw.WriteError(w, r, mw.InvalidField("password"))
		return
	}
	if len(req.AgreementVersion) > 16 {
		mw.WriteError(w, r, mw.InvalidField("agreement_version"))
		return
	}
	nickname := strings.TrimSpace(req.Nickname)
	if len([]rune(nickname)) > 64 {
		mw.WriteError(w, r, mw.InvalidField("nickname"))
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		s.errorLog(r.Context(), "password_hash_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	uid, apiErr := s.insertUser(r.Context(), r, ident, nickname, req.AgreementVersion, string(hash))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	_, cookie, err := s.Sessions.Issue(r.Context(), uid)
	if err != nil {
		s.errorLog(r.Context(), "session_issue_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeAuthUnavailable))
		return
	}
	s.Sessions.SetCookie(w, cookie)
	_ = mw.WriteJSON(w, http.StatusCreated, registerResponse{
		User: view(uid, ident.valueFor(KindEmail), ident.valueFor(KindPhone),
			nickname, "PERSONAL", "NORMAL"),
	})
}

func (i Identifier) valueFor(kind string) string {
	if i.Kind == kind {
		return i.Value
	}
	return ""
}

// insertUser writes t_user, the empty t_credit_account and the audit row in one
// transaction. The account starts at zero: the trial grant is an explicit user
// action in the second segment, never an automatic registration side effect.
func (s *Service) insertUser(ctx context.Context, r *http.Request, ident Identifier, nickname, agreement string, passwordHash string) (int64, *apierr.APIError) {
	now := s.now().Now().UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.errorLog(ctx, "register_begin_tx_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	uid, err := insertUserRow(ctx, tx, ident, nickname, agreement, passwordHash, now)
	if err != nil {
		if isDuplicate(err) {
			// E.1 has no account_exists code. invalid_request_body is the
			// nearest 400; the receipt records the proposed new code.
			return 0, mw.InvalidField("identifier")
		}
		s.errorLog(ctx, "register_insert_user_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO t_credit_account (user_id, created_at, updated_at) VALUES (?, ?, ?)`,
		uid, now, now,
	); err != nil {
		s.errorLog(ctx, "register_insert_account_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}

	after, err := jsonx.Marshal(map[string]string{
		"identifier":        maskIdentifier(ident.Kind, ident.Value),
		"identifier_kind":   ident.Kind,
		"role":              "PERSONAL",
		"status":            "NORMAL",
		"agreement_version": agreement,
	})
	if err != nil {
		return 0, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	if err := s.Audit.Insert(ctx, tx, audit.Entry{
		OperatorID:   uid,
		OperatorRole: "PERSONAL",
		OperatorIP:   audit.IPBytes(r),
		Module:       "USER",
		Action:       "CREATE",
		TargetType:   "t_user",
		TargetID:     strconv.FormatInt(uid, 10),
		After:        after,
		Result:       "OK",
	}); err != nil {
		s.errorLog(ctx, "register_audit_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	if err := tx.Commit(); err != nil {
		s.errorLog(ctx, "register_commit_failed: "+err.Error())
		return 0, apierr.New(apierr.CodeInternalError)
	}
	return uid, nil
}

func insertUserRow(ctx context.Context, tx *sql.Tx, ident Identifier, nickname, agreement, hash string, now time.Time) (int64, error) {
	const q = `INSERT INTO t_user
		(user_no, email, phone, password_hash, password_algo, password_updated_at,
		 nickname, role, status, agreement_version, agreed_at, created_at, updated_at)
		VALUES (?,?,?,?,'bcrypt',?,?,'PERSONAL','NORMAL',?,?,?,?)`
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		userNo, err := newUserNo(now)
		if err != nil {
			return 0, err
		}
		var email, phone any
		if ident.Kind == KindEmail {
			email = ident.Value
		} else {
			phone = ident.Value
		}
		var agreedAt any
		if agreement != "" {
			agreedAt = now
		}
		res, err := tx.ExecContext(ctx, q,
			userNo, email, phone, hash, now, nickname, agreement, agreedAt, now, now)
		if err == nil {
			return res.LastInsertId()
		}
		lastErr = err
		if !isDuplicate(err) {
			return 0, err
		}
		// A duplicate on uk_user_email/uk_user_phone must not be retried with a
		// new user_no: the caller returns "already registered". Retrying only
		// helps a user_no collision.
		if !strings.Contains(err.Error(), "uk_user_no") {
			return 0, err
		}
	}
	return 0, lastErr
}

// newUserNo builds a 12-character user number: "U" + yyyymmdd + three random
// digits. t_user.user_no is char(12); the TDS comment "U+yyyymmdd+4位序号"
// would be 13 characters and does not fit. The receipt records this erratum.
func newUserNo(now time.Time) (string, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("control: random user_no: %w", err)
	}
	n := (int(b[0])<<8 | int(b[1])) % 1000
	return fmt.Sprintf("U%s%03d", now.Format("20060102"), n), nil
}

func validatePassword(pw string) error {
	if strings.TrimSpace(pw) == "" {
		return errors.New("password is empty")
	}
	if len(pw) < 8 {
		return errors.New("password is shorter than 8 bytes")
	}
	// bcrypt silently truncates at 72 bytes; rejecting is the only way to make
	// the stored credential match what the user believes it is.
	if len(pw) > 72 {
		return errors.New("password exceeds bcrypt's 72-byte limit")
	}
	return nil
}

func isDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}
