package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/control/ticket"
	"yunshu.dev/core/internal/redisx"
)

func newCancelService(t *testing.T) (*Service, *mw.SessionManager, *redisx.Client) {
	t.Helper()
	svc, mgr, rdb := newAuthService(t)
	tickets := ticket.New(testDB, svc.Audit, svc.Now, nil)
	svc.CancellableCheck = tickets.CheckAccountCancellable
	return svc, mgr, rdb
}

func seedTicket(t *testing.T, uid int64, state string) string {
	t.Helper()
	now := time.Now().UTC()
	ticketNo := fmt.Sprintf("TK%s%04d", now.Format("20060102150405"), now.UnixNano()%10000)
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_ticket
			(ticket_no, user_id, ticket_type, title, description, state,
			 first_response_due_at, created_at, updated_at)
		VALUES (?,?, 'OTHER', '注销拦截', 'leave this ticket open', ?, ?, ?, ?)`,
		ticketNo, uid, state, now.Add(4*time.Hour), now, now)
	if err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("ticket id: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_ticket_reply WHERE ticket_id = ?`, id)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_ticket WHERE id = ?`, id)
	})
	return ticketNo
}

func runCancel(svc *Service, sess *mw.Session, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/cancel", nil)
	req.RemoteAddr = "198.51.100.77:12345"
	req = req.WithContext(mw.WithSession(req.Context(), sess))
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	svc.Cancel(rec, req)
	return rec
}

// A13 first half: an open (RESPONDED or OPEN) ticket blocks cancellation and
// the refusal lists every blocking ticket number.
func TestAuthCancelBlockedByOpenTicket(t *testing.T) {
	svc, mgr, _ := newCancelService(t)
	uid := newAuthUser(t, uniqueEmail())
	ticketNo := seedTicket(t, uid, ticket.StateResponded)

	sess, _, err := mgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	rec := runCancel(svc, sess, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel with open ticket status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != apierr.CodeResourceStateConflict {
		t.Fatalf("code=%q, want %s", body.Error.Code, apierr.CodeResourceStateConflict)
	}
	if !strings.Contains(body.Error.Message, ticketNo) {
		t.Fatalf("message=%q does not list ticket %s", body.Error.Message, ticketNo)
	}

	var status string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT status FROM t_user WHERE id = ?`, uid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "NORMAL" {
		t.Fatalf("status=%s after blocked cancellation, want NORMAL", status)
	}
	t.Logf("A13 blocked: 409 %s; message=%q; account still %s",
		body.Error.Code, body.Error.Message, status)
}

// A13 second half: a successful cancellation disables the account, deletes
// every K20/K30 key and makes the old sid fail immediately.
func TestAuthCancelRevokesSession(t *testing.T) {
	svc, mgr, rdb := newCancelService(t)
	uid := newAuthUser(t, uniqueEmail())

	sess, cookie, err := mgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	// The sid is valid before cancellation.
	probe := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	probe.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookie})
	if loaded, apiErr := mgr.Load(context.Background(), probe); apiErr != nil || loaded.UID != uid {
		t.Fatalf("precondition: load uid=%d err=%v", loaded.UID, apiErr)
	}

	rec := runCancel(svc, sess, cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cancel status=%d body=%s, want 204", rec.Code, rec.Body.String())
	}

	var status, source string
	var cancelAt time.Time
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT status, disable_source, cancel_apply_at
		  FROM t_user WHERE id = ?`, uid).Scan(&status, &source, &cancelAt); err != nil {
		t.Fatal(err)
	}
	if status != "DISABLED" || source != cancelDisableSource || cancelAt.IsZero() {
		t.Fatalf("user after cancel: status=%s source=%s cancel_at=%v", status, source, cancelAt)
	}

	ctx := context.Background()
	if n, err := rdb.Raw().Exists(ctx, redisx.AdminSessionKey(sess.SID)).Result(); err != nil || n != 0 {
		t.Fatalf("K20 still exists=%d err=%v", n, err)
	}
	if n, err := rdb.Raw().Exists(ctx, redisx.AdminUserSessionSetKey(uid)).Result(); err != nil || n != 0 {
		t.Fatalf("K30 still exists=%d err=%v", n, err)
	}

	// The old cookie can no longer load a session.
	after := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	after.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookie})
	if _, apiErr := mgr.Load(ctx, after); apiErr == nil {
		t.Fatal("old sid still loads after cancellation")
	} else if apiErr.Code != apierr.CodeSessionExpired {
		t.Fatalf("old sid code=%q, want %s", apiErr.Code, apierr.CodeSessionExpired)
	}

	var audits int64
	if err := testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE module = 'USER' AND action = 'CANCEL' AND target_type = 't_user'
		   AND target_id = ? AND result = 'OK'`, fmt.Sprint(uid)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("CANCEL audit rows=%d, want 1", audits)
	}
	t.Logf("A13 revoke: status=%s source=%s; K20/K30 deleted; old sid -> %s; audit=%d",
		status, source, apierr.CodeSessionExpired, audits)
}
