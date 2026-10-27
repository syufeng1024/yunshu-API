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
