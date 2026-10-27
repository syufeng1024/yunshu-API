package usage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/ctltest"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/redisx/redistest"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := redistest.Setup(m)
	_ = db.Close()
	os.Exit(code)
}

// testBase is the frozen instant every usage test starts from. It is the same
// instant the logq tests use, so a failure report can be compared across the
// two packages without timezone arithmetic.
var testBase = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.UTC()
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t.UTC()
}

func newTestService(t *testing.T, clk clock.Clock) *Service {
	t.Helper()
	rdb := redisx.New(redisx.Options{
		Addr: redistest.Addr(), DB: redistest.DB(), PoolSize: 5,
	})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := New(testDB, rdb, clk, nil)
	svc.HMACKey = []byte("t14-usage-unit-test-key")
	svc.Exporter = &FileExporter{Dir: t.TempDir()}
	return svc
}

var userSeq uint64

func newTestUser(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	userSeq++
	now := time.Now()
	var uid int64
	var lastErr error
	for attempt := 0; attempt < 5 && uid == 0; attempt++ {
		var noise [4]byte
		_, _ = rand.Read(noise[:])
		userNo := fmt.Sprintf("U%s%03d", now.Format("20060102"),
			(int(binary.BigEndian.Uint32(noise[:]))+int(userSeq))%1000)
		email := fmt.Sprintf("t14-usage-%d-%d-%d@test.local",
			now.UnixNano(), userSeq, attempt)
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO t_user
				(user_no, email, password_hash, password_algo, password_updated_at,
				 nickname, role, status, created_at, updated_at)
			VALUES (?,?,'not-a-real-hash','bcrypt',?,'t14-usage','PERSONAL','NORMAL',?,?)`,
			userNo, email, now, now, now)
		if err != nil {
			lastErr = err
			if !strings.Contains(err.Error(), "uk_user_no") {
				t.Fatalf("insert user: %v", err)
			}
			continue
		}
		uid, err = res.LastInsertId()
		if err != nil {
			t.Fatalf("user id: %v", err)
		}
	}
	if uid == 0 {
		t.Fatalf("insert user after retries: %v", lastErr)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_usage_record WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx,
			`DELETE FROM t_metric_daily WHERE scope_type = 'USER' AND scope_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}

func newRequestID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("u14-%032x", binary.BigEndian.Uint64(raw[:]))
}

type usageSeed struct {
	requestID   string
	uid         int64
	modelID     int64
	model       string
	channelID   int64
	channelName string
	result      string
	httpCode    int
	tokensIn    int64
	tokensOut   int64
	consumed    int64
	recharge    int64
	bonus       int64
	createdAt   time.Time
}

func seedUsage(t *testing.T, in usageSeed) {
	t.Helper()
	if in.requestID == "" {
		in.requestID = newRequestID()
	}
	if in.model == "" {
		in.model = "gpt-4o-mini"
	}
	if in.modelID == 0 {
		in.modelID = 3
	}
	if in.result == "" {
		in.result = "SUCCESS"
	}
	if in.httpCode == 0 {
		in.httpCode = http.StatusOK
	}
	var (
		channelID   any = in.channelID
		channelName any = in.channelName
		failStage   any
		failOwner   any
	)
	if in.channelName == "" {
		channelName = nil
	}
	if in.result != "SUCCESS" {
		failStage = "UPSTREAM"
		failOwner = "UPSTREAM"
		channelID = nil
		channelName = nil
	}
	_, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_usage_record
			(request_id, user_id, api_key_id, api_key_name_snap, model_id,
			 model_name_snap, request_model_req, inbound_protocol,
			 channel_id, channel_name_snap, outbound_protocol,
			 group_code_snap, is_stream, http_status, biz_code, result,
			 fail_stage, fail_owner, tokens_in, tokens_out, prehold_micro,
			 consumed_micro, consumed_recharge_micro, consumed_bonus_micro,
			 upstream_attempts, ttft_ms, total_ms, upstream_ms, client_ip,
			 client_aborted, full_log_saved, created_at)
		VALUES (?,?,11,'t14-key',?,?,?,'OPENAI_COMPAT',?,?,'OPENAI_COMPAT',
				'standard',1,?,'',?,?,?,?,?,500000,?,?,?,1,90,1500,1200,
				INET6_ATON('203.0.113.7'),0,0,?)`,
		in.requestID, in.uid, in.modelID, in.model, in.model,
		channelID, channelName, in.httpCode, in.result, failStage, failOwner,
		in.tokensIn, in.tokensOut, in.consumed, in.recharge, in.bonus,
		in.createdAt)
	if err != nil {
		t.Fatalf("seed usage %s: %v", in.requestID, err)
	}
}

// call dispatches a handler directly with a synthetic session. pathValues
// feeds mux-style path parameters (ExportToken reads "token").
func call(t *testing.T, h http.HandlerFunc, method, target string, pathValues map[string]string, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	ctx := mw.WithSession(req.Context(), &mw.Session{UID: uid, Role: "PERSONAL"})
	ctx = obslog.WithMeta(ctx, obslog.Meta{RequestID: "t14-usage-fixed-request-id"})
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))
	return rec
}

func callHandler(t *testing.T, h http.HandlerFunc, target string, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, h, http.MethodGet, target, nil, uid)
}

type apiErrorBody struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) apiErrorBody {
	t.Helper()
	var body apiErrorBody
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body (%d %s): %v", rec.Code, rec.Body.String(), err)
	}
	return body
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := jsonx.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response (%d %s): %v", rec.Code, rec.Body.String(), err)
	}
}
