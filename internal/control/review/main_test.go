package review

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/ctltest"
	"yunshu.dev/core/internal/control/fund"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "review tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}

// fixedClock is mutable so a test can advance "now" without sleeping.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFixedClock(t time.Time) *fixedClock { return &fixedClock{t: t} }

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.UTC()
}

func (c *fixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var testBase = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)

func newTestService(t *testing.T, clk clock.Clock) *Service {
	t.Helper()
	auditWriter := &audit.Writer{DB: testDB, Now: clk}
	funds := fund.New(testDB, nil, nil, auditWriter, clk, nil)
	return New(testDB, auditWriter, clk, nil, funds)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

var userSeq uint64

func newTestUser(t *testing.T, role string) int64 {
	t.Helper()
	ctx := context.Background()
	userSeq++
	now := time.Now()
	var uid int64
	for attempt := 0; attempt < 5 && uid == 0; attempt++ {
		userNo := fmt.Sprintf("R%s%03d", now.Format("20060102"), (int(now.UnixNano())+int(userSeq)+attempt)%1000)
		email := fmt.Sprintf("t16-review-%d-%d-%d@test.local", time.Now().UnixNano(), userSeq, attempt)
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO t_user
				(user_no, email, password_hash, password_algo, password_updated_at,
				 nickname, role, status, created_at, updated_at)
			VALUES (?,?,'not-a-real-hash','bcrypt',?,?,?,'NORMAL',?,?)`,
			userNo, email, now, "t16-review", role, now, now)
		if err != nil {
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
		t.Fatal("insert user after retries")
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_review_task WHERE applicant_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_rate_limit_policy WHERE scope_type = 'USER' AND scope_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_notification WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_quota_apply WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_refund_order WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_invoice_apply WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_admin_audit_log WHERE operator_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}

// seedQuotaApply inserts one PENDING application and returns its id.
func seedQuotaApply(t *testing.T, uid int64, targetMicro int64, createdAt time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_quota_apply
			(apply_no, user_id, current_quota_micro, used_at_apply_micro,
			 target_quota_micro, apply_reason, state, created_at, updated_at)
		VALUES (?,?,?,?,?,?, 'PENDING', ?,?)`,
		fmt.Sprintf("QA%s%06d", createdAt.Format("20060102150405"), uid%1000000),
		uid, 100000, 0, targetMicro, "测试提额理由", createdAt, createdAt)
	if err != nil {
		t.Fatalf("seed quota apply: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quota id: %v", err)
	}
	return id
}

// seedReviewTask inserts one queue row and registers cleanup.
func seedReviewTask(t *testing.T, bizType string, bizID, applicantID int64, state string, createdAt time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_review_task (biz_type, biz_id, applicant_id, state, created_at)
		VALUES (?,?,?,?,?)`, bizType, bizID, applicantID, state, createdAt)
	if err != nil {
		t.Fatalf("seed review task: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("review id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_review_task WHERE id = ?`, id)
	})
	return id
}

type apiErrorBody struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

func invoke(t *testing.T, h http.HandlerFunc, method, target string, pathVals map[string]string, body any, uid int64, role string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = jsonx.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	for k, v := range pathVals {
		req.SetPathValue(k, v)
	}
	req = req.WithContext(mw.WithSession(req.Context(), &mw.Session{UID: uid, Role: role}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func callReview(t *testing.T, h http.HandlerFunc, method, target string, id string, body any, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	path := map[string]string{}
	if id != "" {
		path["id"] = id
	}
	return invoke(t, h, method, target, path, body, uid, "FINANCE")
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := jsonx.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response (%d %s): %v", rec.Code, rec.Body.String(), err)
	}
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) apiErrorBody {
	t.Helper()
	var body apiErrorBody
	decodeBody(t, rec, &body)
	return body
}

func quotaState(t *testing.T, id int64) (string, sql.NullString) {
	t.Helper()
	var state string
	var reason sql.NullString
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state, reject_reason FROM t_quota_apply WHERE id = ?`, id).Scan(&state, &reason); err != nil {
		t.Fatalf("quota state: %v", err)
	}
	return state, reason
}

func reviewState(t *testing.T, id int64) (string, sql.NullTime, sql.NullString) {
	t.Helper()
	var state string
	var decided sql.NullTime
	var note sql.NullString
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state, decided_at, decision_note FROM t_review_task WHERE id = ?`, id).
		Scan(&state, &decided, &note); err != nil {
		t.Fatalf("review state: %v", err)
	}
	return state, decided, note
}

func countQuotaPolicies(t *testing.T, uid int64) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ? AND source = 'QUOTA_APPLY'`, uid).Scan(&n); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	return n
}

func countReviewAudit(t *testing.T, reviewID int64, action, result string) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE module = 'REVIEW' AND action = ? AND result = ? AND target_id = ?`,
		action, result, fmt.Sprintf("%d", reviewID)).Scan(&n); err != nil {
		t.Fatalf("count review audit: %v", err)
	}
	return n
}
