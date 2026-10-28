package console

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/ctltest"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "console tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}

var testSecret = []byte(strings.Repeat("p", MinSecretBytes))

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// seedUser inserts one PERSONAL account with a zero balance. The playground
// never reads the balance; P10 does, and the fake gateway in these tests stands
// in for the whole pipeline.
func seedUser(t *testing.T) int64 {
	t.Helper()
	now := time.Now().UTC()
	userNo := "U" + randomHex(t, 5) + fmt.Sprint(time.Now().UnixNano()%1000)
	if len(userNo) > 12 {
		userNo = userNo[:12]
	}
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_user
			(user_no, email, password_hash, password_algo, password_updated_at,
			 nickname, role, status, created_at, updated_at)
		VALUES (?,?,'x','bcrypt',?,'pg','PERSONAL','NORMAL',?,?)`,
		userNo, "pg-"+randomHex(t, 8)+"@test.local", now, now, now)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	uid, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("user id: %v", err)
	}
	if _, err := testDB.ExecContext(context.Background(),
		`INSERT INTO t_credit_account (user_id, created_at, updated_at) VALUES (?,?,?)`,
		uid, now, now); err != nil {
		t.Fatalf("insert credit account: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_api_key_ip_rule WHERE api_key_id IN (SELECT id FROM t_api_key WHERE user_id = ?)`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_api_key_model WHERE api_key_id IN (SELECT id FROM t_api_key WHERE user_id = ?)`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_api_key_audit WHERE api_key_id IN (SELECT id FROM t_api_key WHERE user_id = ?)`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_api_key WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_credit_account WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}

func seedKey(t *testing.T, uid int64, status string, expire *time.Time, withIPRule bool) int64 {
	t.Helper()
	now := time.Now().UTC()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_api_key
			(user_id, key_name, key_prefix, key_hash, key_last4, status, group_code,
			 expire_at, created_at, updated_at)
		VALUES (?,?,?,?,'abcd',?,'standard',?,?,?)`,
		uid, "playground-key", "sk-test1", randomHex(t, 32), status, expire, now, now)
	if err != nil {
		t.Fatalf("insert key: %v", err)
	}
	kid, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("key id: %v", err)
	}
	if withIPRule {
		if _, err := testDB.ExecContext(context.Background(), `
			INSERT INTO t_api_key_ip_rule (api_key_id, cidr, ip_family, note, created_at)
			VALUES (?, '10.0.0.0/8', 4, 'pg-test', ?)`, kid, now); err != nil {
			t.Fatalf("insert ip rule: %v", err)
		}
	}
	return kid
}

func sessionContext(r *http.Request, uid int64) *http.Request {
	return r.WithContext(mw.WithSession(r.Context(), &mw.Session{UID: uid, Role: "PERSONAL"}))
}

func envelope(keyID int64, body string) string {
	return fmt.Sprintf(`{"key_id":%d,"body":%s}`, keyID, body)
}

const chatBody = `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}],"temperature":0.2}`

func postToService(t *testing.T, svc *Service, uid, keyID int64, rid, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/console/playground/chat",
		strings.NewReader(envelope(keyID, body)))
	req = sessionContext(req, uid)
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	svc.Chat(rec, req)
	return rec
}

// fakeGateway verifies the signature exactly as gatewayd does and then hands
// the request to inner. A verification failure is a test failure, not a 401.
func fakeGateway(t *testing.T, secret []byte, inner func(http.ResponseWriter, *http.Request, gw.InternalClaims)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != gw.InternalPath {
			t.Errorf("gateway got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read gateway body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		claims, err := gw.VerifyInternal(secret, time.Now().UTC().Unix(), raw, r.Header)
		if err != nil {
			t.Errorf("gateway signature verification failed: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The read above consumed the body; restore it for inner.
		r.Body = io.NopCloser(bytes.NewReader(raw))
		inner(w, r, claims)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newService(url string, secret string) *Service {
	return New(Deps{DB: testDB, GatewayURL: url, Secret: secret, Now: clock.Real{}})
}

func TestPlaygroundSignsClaimsAndRelaysResponse(t *testing.T) {
	uid := seedUser(t)
	kid := seedKey(t, uid, "ENABLED", nil, true)

	var gotClaims gw.InternalClaims
	var gotBody []byte
	gwSrv := fakeGateway(t, testSecret, func(w http.ResponseWriter, r *http.Request, claims gw.InternalClaims) {
		gotClaims = claims
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Credits-Consumed", "42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1"}`))
	})

	svc := newService(gwSrv.URL, string(testSecret))
	rec := postToService(t, svc, uid, kid, "rid-pg-ok", chatBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"id":"chatcmpl-1"}` {
		t.Fatalf("body = %q", got)
	}
	if got := rec.Header().Get("X-Credits-Consumed"); got != "42" {
		t.Fatalf("X-Credits-Consumed = %q", got)
	}

	// KeyID must be the real row id and the body must arrive byte for byte.
	if gotClaims.KeyID != kid || gotClaims.UserID != uid {
		t.Fatalf("claims identity = key %d user %d, want key %d user %d",
			gotClaims.KeyID, gotClaims.UserID, kid, uid)
	}
	if gotClaims.GroupCode != "standard" || gotClaims.GroupRatioBP != 10000 {
		t.Fatalf("claims group = %q/%d", gotClaims.GroupCode, gotClaims.GroupRatioBP)
	}
	if gotClaims.KeyName != "playground-key" {
		t.Fatalf("claims key name = %q", gotClaims.KeyName)
	}
	if gotClaims.IPMRuleVer == 0 {
		t.Fatal("ip_rule_ver was not read from t_api_key_ip_rule")
	}
	if string(gotBody) != chatBody {
		t.Fatalf("forwarded body = %s, want %s", gotBody, chatBody)
	}

	// No billing bypass: controld wrote no usage row for this request.
	var usageRows int
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_usage_record WHERE request_id = ?`, "rid-pg-ok").Scan(&usageRows); err != nil {
		t.Fatalf("count usage rows: %v", err)
	}
	if usageRows != 0 {
		t.Fatalf("controld wrote %d t_usage_record rows", usageRows)
	}
}

func TestPlaygroundForeignKeyIs404(t *testing.T) {
	owner := seedUser(t)
	other := seedUser(t)
	kid := seedKey(t, owner, "ENABLED", nil, false)

	var calls atomic.Int64
	gwSrv := fakeGateway(t, testSecret, func(w http.ResponseWriter, _ *http.Request, _ gw.InternalClaims) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	svc := newService(gwSrv.URL, string(testSecret))

	for _, id := range []int64{kid, kid + 1_000_000} {
		rec := postToService(t, svc, other, id, "rid-pg-foreign", chatBody)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("key %d: status = %d, body %s", id, rec.Code, rec.Body.String())
		}
		var decoded map[string]any
		if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if code := decoded["error"].(map[string]any)["code"]; code != "key_not_found" {
			t.Fatalf("code = %v", code)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("gateway was called %d times for foreign keys", calls.Load())
	}
}

func TestPlaygroundDisabledKeyIs409(t *testing.T) {
	uid := seedUser(t)
	kid := seedKey(t, uid, "DISABLED", nil, false)
	svc := newService("http://127.0.0.1:1", string(testSecret))

	rec := postToService(t, svc, uid, kid, "rid-pg-disabled", chatBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if code := decoded["error"].(map[string]any)["code"]; code != "resource_state_conflict" {
		t.Fatalf("code = %v", code)
	}
}

func TestPlaygroundExpiredKeyIs401(t *testing.T) {
	uid := seedUser(t)
	past := time.Now().UTC().Add(-time.Minute)
	kid := seedKey(t, uid, "ENABLED", &past, false)

	var calls atomic.Int64
	gwSrv := fakeGateway(t, testSecret, func(w http.ResponseWriter, _ *http.Request, _ gw.InternalClaims) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	svc := newService(gwSrv.URL, string(testSecret))

	rec := postToService(t, svc, uid, kid, "rid-pg-expired", chatBody)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if code := decoded["error"].(map[string]any)["code"]; code != "key_expired" {
		t.Fatalf("code = %v", code)
	}
	if calls.Load() != 0 {
		t.Fatalf("gateway was called %d times for an expired key", calls.Load())
	}
}

// A 402 from P09/P10 is relayed with its status, body and credit headers; the
// console does not reinterpret it.
func TestPlaygroundRelaysInsufficientCredits(t *testing.T) {
	uid := seedUser(t)
	kid := seedKey(t, uid, "ENABLED", nil, false)
	gwSrv := fakeGateway(t, testSecret, func(w http.ResponseWriter, r *http.Request, _ gw.InternalClaims) {
		e := apierr.NewInsufficientCredits(1_000_000, 10, 50).
			WithRequestID(r.Header.Get("X-Request-Id"))
		_ = apierr.Write(w, e)
	})
	svc := newService(gwSrv.URL, string(testSecret))

	rec := postToService(t, svc, uid, kid, "rid-pg-402", chatBody)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eb := decoded["error"].(map[string]any)
	if eb["code"] != "insufficient_credits" {
		t.Fatalf("code = %v", eb["code"])
	}
	if eb["request_id"] != "rid-pg-402" {
		t.Fatalf("body request_id = %v", eb["request_id"])
	}
	if got := rec.Header().Get("X-Credits-Remaining"); got != "10" {
		t.Fatalf("X-Credits-Remaining = %q", got)
	}
}

func TestPlaygroundGatewayUnreachableIs502(t *testing.T) {
	uid := seedUser(t)
	kid := seedKey(t, uid, "ENABLED", nil, false)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadURL := "http://" + ln.Addr().String()
	_ = ln.Close()

	svc := newService(deadURL, string(testSecret))
	rec := postToService(t, svc, uid, kid, "rid-pg-502", chatBody)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if code := decoded["error"].(map[string]any)["code"]; code != "upstream_all_failed" {
		t.Fatalf("code = %v", code)
	}
}

func TestPlaygroundUnconfiguredIs503(t *testing.T) {
	uid := seedUser(t)
	kid := seedKey(t, uid, "ENABLED", nil, false)

	cases := []struct {
		name string
		url  string
		sec  string
	}{
		{name: "no secret", url: "http://127.0.0.1:1", sec: ""},
		{name: "short secret", url: "http://127.0.0.1:1", sec: "short"},
		{name: "no url", url: "", sec: string(testSecret)},
		{name: "bad url", url: "gatewayd:8080", sec: string(testSecret)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(tc.url, tc.sec)
			rec := postToService(t, svc, uid, kid, "rid-pg-503", chatBody)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			var decoded map[string]any
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if code := decoded["error"].(map[string]any)["code"]; code != "config_unavailable" {
				t.Fatalf("code = %v", code)
			}
		})
	}
}

// TestPlaygroundStreamsSSEWithoutBuffering is the anti-buffering proof. The
// fake gateway emits the first frame, flushes, and then blocks. The test client
// must receive that frame while the gateway is still blocked; an io.ReadAll
// relay would keep the console response head uncommitted (or the body empty)
// until the gate is released.
func TestPlaygroundStreamsSSEWithoutBuffering(t *testing.T) {
	uid := seedUser(t)
	kid := seedKey(t, uid, "ENABLED", nil, false)

	release := make(chan struct{})
	gwSrv := fakeGateway(t, testSecret, func(w http.ResponseWriter, _ *http.Request, _ gw.InternalClaims) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"chunk\":1}\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	})

	svc := newService(gwSrv.URL, string(testSecret))
	consoleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc.Chat(w, sessionContext(r, uid))
	}))
	defer consoleSrv.Close()

	req, err := http.NewRequest(http.MethodPost, consoleSrv.URL+"/api/console/playground/chat",
		strings.NewReader(envelope(kid, `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Request-Id", "rid-pg-sse")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}

	reader := bufio.NewReader(resp.Body)
	firstLine := make(chan string, 1)
	go func() {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			firstLine <- "ERR: " + readErr.Error()
			return
		}
		firstLine <- line
	}()

	select {
	case line := <-firstLine:
		if !strings.Contains(line, `"chunk":1`) {
			t.Fatalf("first frame = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first SSE frame was buffered: nothing arrived while the gateway was blocked")
	}

	close(release)
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if !bytes.Contains(rest, []byte("[DONE]")) {
		t.Fatalf("tail = %q", rest)
	}
}
