package control

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/ctltest"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/redisx/redistest"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "control tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := redistest.Setup(m)
	_ = db.Close()
	os.Exit(code)
}

// adminRoutes is the T15 admin surface. The test sends a PERSONAL session to
// every one of them; the middleware must answer 403 before any handler runs.
var adminRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/admin/whoami"},
	{http.MethodGet, "/api/admin/channels"},
	{http.MethodGet, "/api/admin/channels/1"},
	{http.MethodPost, "/api/admin/channels"},
	{http.MethodPatch, "/api/admin/channels/1"},
	{http.MethodPost, "/api/admin/channels/1/disable"},
	{http.MethodDelete, "/api/admin/channels/1"},
	{http.MethodGet, "/api/admin/models"},
	{http.MethodGet, "/api/admin/models/1"},
	{http.MethodPost, "/api/admin/models"},
	{http.MethodPatch, "/api/admin/models/1"},
	{http.MethodPost, "/api/admin/models/1/listing-state"},
	{http.MethodPost, "/api/admin/models/1/recommend-weight"},
	{http.MethodGet, "/api/admin/models/1/aliases"},
	{http.MethodPost, "/api/admin/models/1/aliases"},
	{http.MethodDelete, "/api/admin/models/1/aliases/qwen-max"},
	{http.MethodGet, "/api/admin/prices"},
	{http.MethodPost, "/api/admin/prices"},
	{http.MethodPatch, "/api/admin/prices/1"},
	{http.MethodGet, "/api/admin/groups"},
	{http.MethodPatch, "/api/admin/groups/1"},
	{http.MethodGet, "/api/admin/announcements"},
	{http.MethodPost, "/api/admin/announcements"},
	{http.MethodPatch, "/api/admin/announcements/1"},
	{http.MethodPost, "/api/admin/announcements/1/publish"},
	{http.MethodGet, "/api/admin/risk/rules"},
	{http.MethodPost, "/api/admin/risk/rules"},
	{http.MethodPatch, "/api/admin/risk/rules/1"},
	{http.MethodDelete, "/api/admin/risk/rules/1"},
	{http.MethodGet, "/api/admin/risk/hits"},
	{http.MethodPost, "/api/admin/risk/users/1/score"},
	{http.MethodGet, "/api/admin/users"},
	{http.MethodGet, "/api/admin/users/1"},
	{http.MethodPost, "/api/admin/users/1/disable"},
	{http.MethodPost, "/api/admin/users/1/enable"},
	{http.MethodGet, "/api/admin/config"},
	{http.MethodPut, "/api/admin/config"},
	// T16 ticket centre: the operator queue is ticket.reply, which PERSONAL
	// never holds.
	{http.MethodGet, "/api/admin/tickets"},
	{http.MethodPost, "/api/admin/tickets/1/reply"},
	{http.MethodPost, "/api/admin/tickets/1/state"},
	// T16 review centre: fund.approve is FINANCE/SUPER_ADMIN only.
	{http.MethodGet, "/api/admin/reviews"},
	{http.MethodPost, "/api/admin/reviews/1/approve"},
	{http.MethodPost, "/api/admin/reviews/1/reject"},
}

func TestPersonalAccountGets403OnEveryAdminRoute(t *testing.T) {
	rdb := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB(), PoolSize: 5})
	t.Cleanup(func() { _ = rdb.Close() })
	sessMgr, err := mw.NewSessionManager(testDB, rdb, "0123456789abcdef")
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	srv, err := New(Deps{DB: testDB, Redis: rdb, Sessions: sessMgr, Now: clock.Real{}})
	if err != nil {
		t.Fatalf("control server: %v", err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)

	uid := newRoleUser(t, "PERSONAL")
	_, cookieValue, err := sessMgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	for _, route := range adminRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, bytes.NewReader(nil))
			req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookieValue})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Error.Code != "permission_denied" {
				t.Fatalf("code=%q, want permission_denied", body.Error.Code)
			}
		})
	}
}

// Positive control: OPS holds model.read, so the channel list is 200; a
// PERSONAL account is 403 on the same route. Without this pair, the deny test
// above could pass because the whole route table is broken.
func TestOpsCanReadChannels(t *testing.T) {
	rdb := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB(), PoolSize: 5})
	t.Cleanup(func() { _ = rdb.Close() })
	sessMgr, err := mw.NewSessionManager(testDB, rdb, "0123456789abcdef")
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	srv, err := New(Deps{DB: testDB, Redis: rdb, Sessions: sessMgr, Now: clock.Real{}})
	if err != nil {
		t.Fatalf("control server: %v", err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)

	uid := newRoleUser(t, "OPS")
	_, cookieValue, err := sessMgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/channels", nil)
	req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookieValue})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("OPS channel list status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

// The ticket consoles are split: OPS answers through ticket.reply but must not
// open the account-scoped console routes. Ticket ids in the path are irrelevant:
// Require rejects before the handler runs.
func TestOpsCannotReachConsoleTickets(t *testing.T) {
	rdb := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB(), PoolSize: 5})
	t.Cleanup(func() { _ = rdb.Close() })
	sessMgr, err := mw.NewSessionManager(testDB, rdb, "0123456789abcdef")
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	srv, err := New(Deps{DB: testDB, Redis: rdb, Sessions: sessMgr, Now: clock.Real{}})
	if err != nil {
		t.Fatalf("control server: %v", err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)

	uid := newRoleUser(t, "OPS")
	_, cookieValue, err := sessMgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/console/tickets"},
		{http.MethodGet, "/api/console/tickets"},
		{http.MethodGet, "/api/console/tickets/1"},
		{http.MethodPost, "/api/console/tickets/1/close"},
		{http.MethodGet, "/api/console/request-ids"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, bytes.NewReader(nil))
			req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookieValue})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Error.Code != "permission_denied" {
				t.Fatalf("code=%q, want permission_denied", body.Error.Code)
			}
		})
	}
}

// Positive control: the same route table answers a PERSONAL session on the
// console tickets list (the handler then scopes by uid). Without this pair the
// 403 test above could pass because the whole ticket surface is broken.
func TestPersonalCanReachConsoleTickets(t *testing.T) {
	rdb := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB(), PoolSize: 5})
	t.Cleanup(func() { _ = rdb.Close() })
	sessMgr, err := mw.NewSessionManager(testDB, rdb, "0123456789abcdef")
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	srv, err := New(Deps{DB: testDB, Redis: rdb, Sessions: sessMgr, Now: clock.Real{}})
	if err != nil {
		t.Fatalf("control server: %v", err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)

	uid := newRoleUser(t, "PERSONAL")
	_, cookieValue, err := sessMgr.Issue(context.Background(), uid)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/console/tickets", nil)
	req.AddCookie(&http.Cookie{Name: mw.SessionCookieName, Value: cookieValue})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PERSONAL ticket list status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

var authzUserSeq int64

func newRoleUser(t *testing.T, role string) int64 {
	t.Helper()
	now := time.Now()
	authzUserSeq++
	// t_user.user_no is char(12): A + yyyymmdd + 3 digits.
	userNo := fmt.Sprintf("A%s%03d", now.Format("20060102"),
		(now.UnixNano()/1_000_000+authzUserSeq)%1000)
	email := fmt.Sprintf("t15-authz-%d@test.local", now.UnixNano())
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_user
			(user_no, email, password_hash, password_algo, password_updated_at,
			 nickname, role, status, created_at, updated_at)
		VALUES (?,?,'not-a-real-hash','bcrypt',?,'t15-authz',?,'NORMAL',?,?)`,
		userNo, email, now, role, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "uk_user_no") {
			return newRoleUser(t, role)
		}
		t.Fatalf("insert user: %v", err)
	}
	uid, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}
