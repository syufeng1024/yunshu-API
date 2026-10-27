package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/redisx"
)

func TestLoginLocksAccountAfterFiveFailures(t *testing.T) {
	svc, _, rdb := newAuthService(t)
	email := uniqueEmail()
	_ = newAuthUser(t, email)
	ip := uniqueIP()

	for i := 0; i < loginAccountFailLimit; i++ {
		rec := doLogin(t, svc, email, "WrongPassword1", ip)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status=%d body=%s, want 401", i+1, rec.Code, rec.Body.String())
		}
	}

	// Attempt six is correct. The lock must still reject it: a lockout that
	// lets the right password through protects nothing.
	rec := doLogin(t, svc, email, testPassword, ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login status=%d body=%s, want 429", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != apierr.CodeLoginLocked {
		t.Fatalf("code=%q, want %q", body.Error.Code, apierr.CodeLoginLocked)
	}
	if body.Error.Type != apierr.TypeRateLimit {
		t.Fatalf("type=%q, want %q", body.Error.Type, apierr.TypeRateLimit)
	}
	if body.Error.RetryAfterSeconds <= 0 {
		t.Fatalf("retry_after_seconds=%d, want > 0", body.Error.RetryAfterSeconds)
	}
	if !strings.Contains(body.Error.Message, "锁定") || !strings.Contains(body.Error.Hint, "锁定") {
		t.Fatalf("message/hint do not mention the lockout: %q / %q", body.Error.Message, body.Error.Hint)
	}
	t.Logf("6th attempt with correct password -> %d %s, retry_after=%ds, message=%q",
		rec.Code, body.Error.Code, body.Error.RetryAfterSeconds, body.Error.Message)

	// K28 stores the count for the account. The threshold refresh sets the
	// full 15-minute window from the fifth failure.
	n, err := rdb.Raw().Get(context.Background(), redisx.LoginAccountFailKey(email)).Int64()
	if err != nil {
		t.Fatalf("read K28: %v", err)
	}
	if n != loginAccountFailLimit {
		t.Fatalf("K28 count=%d, want %d", n, loginAccountFailLimit)
	}
	ttl, err := rdb.Raw().TTL(context.Background(), redisx.LoginAccountFailKey(email)).Result()
	if err != nil {
		t.Fatalf("read K28 ttl: %v", err)
	}
	if ttl <= 0 || ttl > loginAccountLockWindow {
		t.Fatalf("K28 ttl=%s, want (0, %s]", ttl, loginAccountLockWindow)
	}
}

func TestLoginLockClearsAfterWindow(t *testing.T) {
	svc, _, rdb := newAuthService(t)
	email := uniqueEmail()
	_ = newAuthUser(t, email)
	ip := uniqueIP()

	for i := 0; i < loginAccountFailLimit; i++ {
		_ = doLogin(t, svc, email, "WrongPassword1", ip)
	}
	if rec := doLogin(t, svc, email, testPassword, ip); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("precondition: status=%d body=%s, want 429", rec.Code, rec.Body.String())
	}

	// The window is a Redis EXPIRE, so "waiting 15 minutes" is exactly "the keys
	// are gone". Delete them instead of sleeping; the test asserts the same
	// state the server sees after the TTL fires.
	ctx := context.Background()
	if err := rdb.Raw().Del(ctx,
		redisx.LoginAccountFailKey(email), redisx.LoginIPFailKey(ip)).Err(); err != nil {
		t.Fatalf("clear lock counters: %v", err)
	}

	rec := doLogin(t, svc, email, testPassword, ip)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after window status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

func TestLoginSuccessClearsAccountFailures(t *testing.T) {
	svc, _, rdb := newAuthService(t)
	email := uniqueEmail()
	_ = newAuthUser(t, email)
	ip := uniqueIP()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if rec := doLogin(t, svc, email, "WrongPassword1", ip); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status=%d body=%s, want 401", i+1, rec.Code, rec.Body.String())
		}
	}
	if n, err := rdb.Raw().Exists(ctx, redisx.LoginAccountFailKey(email)).Result(); err != nil || n != 1 {
		t.Fatalf("precondition: K28 exists=%d err=%v, want 1", n, err)
	}

	if rec := doLogin(t, svc, email, testPassword, ip); rec.Code != http.StatusOK {
		t.Fatalf("successful login status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if n, err := rdb.Raw().Exists(ctx, redisx.LoginAccountFailKey(email)).Result(); err != nil || n != 0 {
		t.Fatalf("K28 after success exists=%d err=%v, want 0", n, err)
	}

	// Two more failures would have crossed the threshold if the counter had
	// survived (3 + 2 = 5). The correct password must still work.
	for i := 0; i < 2; i++ {
		_ = doLogin(t, svc, email, "WrongPassword1", ip)
	}
	if rec := doLogin(t, svc, email, testPassword, ip); rec.Code != http.StatusOK {
		t.Fatalf("login after reset status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

func TestIPFailureBudgetLocksEveryAccount(t *testing.T) {
	svc, _, _ := newAuthService(t)
	ctx := context.Background()
	ip := uniqueIP()

	// The IP budget is independent of the account counter: twenty failures
	// spread over twenty identifiers still exhaust the address.
	for i := 0; i < loginIPFailLimit; i++ {
		svc.recordLoginFailure(ctx, Identifier{
			Kind:  KindEmail,
			Value: fmt.Sprintf("missing-%s-%d@test.local", ip, i),
		}, ip)
	}

	email := uniqueEmail()
	_ = newAuthUser(t, email)
	rec := doLogin(t, svc, email, testPassword, ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP-locked login status=%d body=%s, want 429", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != apierr.CodeRPMLimitExceeded {
		t.Fatalf("code=%q, want %q", body.Error.Code, apierr.CodeRPMLimitExceeded)
	}
	if body.Error.RetryAfterSeconds <= 0 {
		t.Fatalf("retry_after_seconds=%d, want > 0", body.Error.RetryAfterSeconds)
	}
}

// T11: one 429 is not the other. The account budget (5 failures) blocks the
// login itself and answers login_locked; the IP budget (20 failures) throttles
// the address and keeps rpm_limit_exceeded. Before the split, a client that hit
// the account lock saw a rate-limit code and would have backed off and retried
// while the lock was still in force.
func TestAccountLockAndIPLockUseDifferentCodes(t *testing.T) {
	svc, _, _ := newAuthService(t)
	ctx := context.Background()

	lockedEmail := uniqueEmail()
	_ = newAuthUser(t, lockedEmail)
	accountIP := uniqueIP()
	for i := 0; i < loginAccountFailLimit; i++ {
		_ = doLogin(t, svc, lockedEmail, "WrongPassword1", accountIP)
	}
	accountRec := doLogin(t, svc, lockedEmail, testPassword, accountIP)
	accountBody := decodeError(t, accountRec)
	if accountBody.Error.Code != apierr.CodeLoginLocked {
		t.Fatalf("account lock code=%q, want %q", accountBody.Error.Code, apierr.CodeLoginLocked)
	}

	// Twenty failures spread over twenty identifiers exhaust the address
	// without locking any single account.
	ipLocked := uniqueIP()
	for i := 0; i < loginIPFailLimit; i++ {
		svc.recordLoginFailure(ctx, Identifier{
			Kind:  KindEmail,
			Value: fmt.Sprintf("missing-%s-%d@test.local", ipLocked, i),
		}, ipLocked)
	}
	email := uniqueEmail()
	_ = newAuthUser(t, email)
	ipRec := doLogin(t, svc, email, testPassword, ipLocked)
	ipBody := decodeError(t, ipRec)
	if ipBody.Error.Code != apierr.CodeRPMLimitExceeded {
		t.Fatalf("IP lock code=%q, want %q", ipBody.Error.Code, apierr.CodeRPMLimitExceeded)
	}
	if accountBody.Error.Code == ipBody.Error.Code {
		t.Fatalf("account and IP locks share code %q", ipBody.Error.Code)
	}
	t.Logf("account lock -> %d %s retry=%ds message=%q; IP lock -> %d %s retry=%ds message=%q",
		accountRec.Code, accountBody.Error.Code, accountBody.Error.RetryAfterSeconds, accountBody.Error.Message,
		ipRec.Code, ipBody.Error.Code, ipBody.Error.RetryAfterSeconds, ipBody.Error.Message)
}

func TestChangePasswordRevokesEverySession(t *testing.T) {
	svc, sessMgr, rdb := newAuthService(t)
	email := uniqueEmail()
	uid := newAuthUser(t, email)
	ctx := context.Background()

	sess1, cookie1, err := sessMgr.Issue(ctx, uid)
	if err != nil {
		t.Fatalf("issue session 1: %v", err)
	}
	sess2, cookie2, err := sessMgr.Issue(ctx, uid)
	if err != nil {
		t.Fatalf("issue session 2: %v", err)
	}
	sess1.Role = "PERSONAL"
	sess2.Role = "PERSONAL"
	for _, sid := range []string{sess1.SID, sess2.SID} {
		if n, err := rdb.Raw().Exists(ctx, redisx.AdminSessionKey(sid)).Result(); err != nil || n != 1 {
			t.Fatalf("precondition: K20 %s exists=%d err=%v", sid, n, err)
		}
	}

	rec := changePasswordRequest(t, svc, sess1, testPassword, testNewPassword)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change password status=%d body=%s, want 204", rec.Code, rec.Body.String())
	}

	for _, sid := range []string{sess1.SID, sess2.SID} {
		if n, err := rdb.Raw().Exists(ctx, redisx.AdminSessionKey(sid)).Result(); err != nil || n != 0 {
			t.Fatalf("K20 %s after change exists=%d err=%v, want 0", sid, n, err)
		}
	}
	if n, err := rdb.Raw().Exists(ctx, redisx.AdminUserSessionSetKey(uid)).Result(); err != nil || n != 0 {
		t.Fatalf("K30 after change exists=%d err=%v, want 0", n, err)
	}

	// Both old sids are now indistinguishable from a logged-out session.
	for _, cookie := range []string{cookie1, cookie2} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookie})
		if _, apiErr := sessMgr.Load(ctx, req); apiErr == nil || apiErr.Code != apierr.CodeSessionExpired {
			t.Fatalf("old sid still valid: err=%v", apiErr)
		}
	}

	// The new password works, the old one does not.
	if rec := doLogin(t, svc, email, testNewPassword, uniqueIP()); rec.Code != http.StatusOK {
		t.Fatalf("login with new password status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if rec := doLogin(t, svc, email, testPassword, uniqueIP()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with old password status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
	t.Logf("password changed: sid1=%s… sid2=%s… both K20 deleted, both Load -> 401 session_expired",
		sess1.SID[:8], sess2.SID[:8])
}

func TestChangePasswordWrongOldKeepsEverything(t *testing.T) {
	svc, sessMgr, rdb := newAuthService(t)
	email := uniqueEmail()
	uid := newAuthUser(t, email)
	ctx := context.Background()

	sess, cookie, err := sessMgr.Issue(ctx, uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	sess.Role = "PERSONAL"

	rec := changePasswordRequest(t, svc, sess, "WrongPassword9", testNewPassword)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong old password status=%d body=%s, want 401", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != apierr.CodeInvalidCredentials {
		t.Fatalf("code=%q, want %q", body.Error.Code, apierr.CodeInvalidCredentials)
	}

	// Nothing changed: the session survives and the old password still works.
	if n, err := rdb.Raw().Exists(ctx, redisx.AdminSessionKey(sess.SID)).Result(); err != nil || n != 1 {
		t.Fatalf("session after rejected change exists=%d err=%v, want 1", n, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookie})
	if _, apiErr := sessMgr.Load(ctx, req); apiErr != nil {
		t.Fatalf("session after rejected change: %v", apiErr)
	}
	if rec := doLogin(t, svc, email, testPassword, uniqueIP()); rec.Code != http.StatusOK {
		t.Fatalf("login with unchanged password status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if rec := doLogin(t, svc, email, testNewPassword, uniqueIP()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("new password was applied despite rejection: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestChangePasswordValidatesNewPassword(t *testing.T) {
	svc, sessMgr, _ := newAuthService(t)
	email := uniqueEmail()
	uid := newAuthUser(t, email)
	sess, _, err := sessMgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	sess.Role = "PERSONAL"

	rec := changePasswordRequest(t, svc, sess, testPassword, "short")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short new password status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Error.Code != apierr.CodeInvalidRequestBody {
		t.Fatalf("code=%q, want %q", body.Error.Code, apierr.CodeInvalidRequestBody)
	}
}
