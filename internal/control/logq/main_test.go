package logq

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
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "logq tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}

// testBase is the frozen instant every logq test starts from. Tests move it
// explicitly so the 180/365/366-day boundaries are exact rather than
// approximated with time.Now().
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
	return New(testDB, nil, clk, nil)
}

var userSeq uint64

func newTestUser(t *testing.T, role string) int64 {
	t.Helper()
	ctx := context.Background()
	userSeq++
	now := time.Now()
	var uid int64
	var lastErr error
	for attempt := 0; attempt < 5 && uid == 0; attempt++ {
		var noise [4]byte
		_, _ = rand.Read(noise[:])
		userNo := fmt.Sprintf("Q%s%03d", now.Format("20060102"),
			(int(binary.BigEndian.Uint32(noise[:]))+int(userSeq))%1000)
		email := fmt.Sprintf("t14-logq-%d-%d-%d@test.local", now.UnixNano(), userSeq, attempt)
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO t_user
				(user_no, email, password_hash, password_algo, password_updated_at,
				 nickname, role, status, created_at, updated_at)
			VALUES (?,?,'not-a-real-hash','bcrypt',?,'t14',?,'NORMAL',?,?)`,
			userNo, email, now, role, now, now)
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
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_upstream_attempt WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_usage_record WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}

// newRequestID returns a 36-character value shaped like a UUIDv7.
// t_usage_record.request_id is char(36).
func newRequestID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("t14-%032x", binary.BigEndian.Uint64(raw[:]))
}

type usageSeed struct {
	requestID string
	uid       int64
	model     string
	result    string
	httpCode  int
	fullLog   bool
	createdAt time.Time
}

func seedUsage(t *testing.T, in usageSeed) {
	t.Helper()
	if in.model == "" {
		in.model = "gpt-4o-mini"
	}
	if in.result == "" {
		in.result = "SUCCESS"
	}
	if in.httpCode == 0 {
		in.httpCode = http.StatusOK
	}
	var (
		failStage   any
		failOwner   any
		channelID   any = 7
		channelName any = "aliyun-primary"
	)
	if in.result == "FAILED" {
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
		VALUES (?,?,11,'t14-key',3,?,?,'OPENAI_COMPAT',?,?,'OPENAI_COMPAT',
				'standard',1,?,'',?,?,?,120,80,500000,
				420000,300000,120000,1,90,1500,1200,INET6_ATON('203.0.113.7'),
				0,?,?)`,
		in.requestID, in.uid, in.model, in.model, channelID, channelName,
		in.httpCode, in.result, failStage, failOwner, in.fullLog, in.createdAt)
	if err != nil {
		t.Fatalf("seed usage %s: %v", in.requestID, err)
	}
}

func seedAttempt(t *testing.T, uid int64, requestID string, index int, result string, httpCode int, connectMs, firstByteMs, totalMs int64) {
	t.Helper()
	_, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_upstream_attempt
			(request_id, attempt_index, user_id, model_id, route_id, channel_id,
			 channel_name_snap, protocol_code, upstream_model, phase, result,
			 http_status, upstream_code, upstream_msg, is_retryable,
			 retry_decision, connect_ms, first_byte_ms, total_ms, tokens_in,
			 tokens_out, bytes_in, bytes_out, error_class, created_at)
		VALUES (?,?,?,3,5,7,'aliyun-primary','OPENAI','gpt-4o-mini-2024-08-06',
				'COMPLETE',?,?,NULL,'seeded attempt',1,'NO_RETRY',?,?,?,120,80,
				2048,4096,NULL,?)`,
		requestID, index, uid, result, httpCode,
		connectMs, firstByteMs, totalMs, testBase)
	if err != nil {
		t.Fatalf("seed attempt %s/%d: %v", requestID, index, err)
	}
}

// invoke dispatches a handler directly with a synthetic session. The fixed
// observation request id makes two error responses byte-comparable (A8).
func invoke(t *testing.T, h http.HandlerFunc, method, target, requestID string, uid int64, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if requestID != "" {
		req.SetPathValue("request_id", requestID)
	}
	ctx := mw.WithSession(req.Context(), &mw.Session{UID: uid, Role: role})
	ctx = obslog.WithMeta(ctx, obslog.Meta{RequestID: "t14-fixed-request-id"})
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))
	return rec
}

func callUser(t *testing.T, h http.HandlerFunc, method, target string, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	return invoke(t, h, method, target, "", uid, "PERSONAL")
}

func callUserID(t *testing.T, h http.HandlerFunc, method, target, requestID string, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	return invoke(t, h, method, target, requestID, uid, "PERSONAL")
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

// fakeBodyStore stands in for t_request_body. T14's first segment cannot
// create that table (T22 migration 0022 owns it), so the success path is
// exercised through this seam while the real store proves the missing-table
// fallback.
type fakeBodyStore struct {
	body      string
	err       error
	calls     int
	lastUID   int64
	lastReqID string
}

func (f *fakeBodyStore) fetch(_ context.Context, uid int64, requestID string) (string, error) {
	f.calls++
	f.lastUID = uid
	f.lastReqID = requestID
	return f.body, f.err
}
