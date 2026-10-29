package auth

// T39 segment 2 (OI-07): control-plane triggers for the two codes produced by
// the login path. Both assertions include the code value; each goes red when
// the corresponding branch is removed.

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/apierr"
)

// account_frozen_due_to_overdraft (login.go:108): a correct password on a
// FROZEN_OVERDRAFT account is refused with 402, not 403 or invalid_credentials.
func TestLoginFrozenDueToOverdraftIs402(t *testing.T) {
	svc, _, _ := newAuthService(t)
	email := uniqueEmail()
	uid := newAuthUser(t, email)
	if _, err := testDB.ExecContext(context.Background(),
		`UPDATE t_user SET status = 'FROZEN_OVERDRAFT' WHERE id = ?`, uid); err != nil {
		t.Fatalf("freeze user: %v", err)
	}

	rec := doLogin(t, svc, email, testPassword, uniqueIP())
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec).Error.Code; got != apierr.CodeAccountFrozenDueToOverdraft {
		t.Fatalf("code = %q, want %s", got, apierr.CodeAccountFrozenDueToOverdraft)
	}
}

// auth_unavailable (login.go:60 and :81): both dependency failures fail closed
// with 503. The Redis case is an absent store; the SQL case is a closed pool.
// Neither path may degrade to invalid_credentials.
func TestLoginAuthUnavailableWhenDependenciesFail(t *testing.T) {
	assertUnavailable := func(t *testing.T, svc *Service) {
		t.Helper()
		rec := doLogin(t, svc, uniqueEmail(), testPassword, uniqueIP())
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec).Error.Code; got != apierr.CodeAuthUnavailable {
			t.Fatalf("code = %q, want %s", got, apierr.CodeAuthUnavailable)
		}
	}

	t.Run("lock store unavailable", func(t *testing.T) {
		svc, _, _ := newAuthService(t)
		svc.Sessions.Redis = nil
		assertUnavailable(t, svc)
	})

	t.Run("user store unavailable", func(t *testing.T) {
		svc, _, _ := newAuthService(t)
		closed, err := sql.Open("mysql", "root:root@tcp(127.0.0.1:1)/yunshu_test?parseTime=true")
		if err != nil {
			t.Fatalf("open placeholder pool: %v", err)
		}
		if err := closed.Close(); err != nil {
			t.Fatalf("close placeholder pool: %v", err)
		}
		svc.DB = closed
		assertUnavailable(t, svc)
	})
}
