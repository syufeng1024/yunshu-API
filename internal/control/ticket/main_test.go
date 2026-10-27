package ticket

import (
	"bytes"
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

	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/ctltest"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ticket tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}

// testBase is the frozen instant every ticket test starts from. Tests advance
// it explicitly, which is what makes the 4-hour SLA assertions exact.
var testBase = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)

// fakeClock is mutable: the SLA tests need to move time forward without
// sleeping, and the service must read the clock on every request.
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

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type sentMail struct {
	To      string
	Subject string
	Body    string
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{To: to, Subject: subject, Body: body})
	return nil
}

func (m *fakeMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *fakeMailer) last() sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return sentMail{}
	}
	return m.sent[len(m.sent)-1]
}

func newTestService(t *testing.T, clk clock.Clock) *Service {
	t.Helper()
	svc := New(testDB, &audit.Writer{DB: testDB, Now: clk}, clk, nil)
	return svc
}

var userSeq uint64

// newTestUser inserts one account. The role only affects requests the test
// dispatches through mw.Require; the service itself takes the uid from the
// session.
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
		userNo := fmt.Sprintf("K%s%03d", now.Format("20060102"),
			(int(binary.BigEndian.Uint32(noise[:]))+int(userSeq))%1000)
		email := fmt.Sprintf("t16-ticket-%d-%d-%d@test.local", now.UnixNano(), userSeq, attempt)
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO t_user
				(user_no, email, password_hash, password_algo, password_updated_at,
				 nickname, role, status, created_at, updated_at)
			VALUES (?,?,'not-a-real-hash','bcrypt',?,'t16',?,'NORMAL',?,?)`,
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
		_, _ = testDB.ExecContext(ctx, `
			DELETE r FROM t_ticket_reply r JOIN t_ticket t ON t.id = r.ticket_id
			 WHERE t.user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `
			DELETE FROM t_admin_audit_log
			 WHERE module = 'TICKET'
			   AND target_id IN (SELECT CAST(id AS CHAR) FROM t_ticket WHERE user_id = ?)`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_ticket WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_content_policy_hit WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_usage_record WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}

func userEmail(t *testing.T, uid int64) string {
	t.Helper()
	var email string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT email FROM t_user WHERE id = ?`, uid).Scan(&email); err != nil {
		t.Fatalf("user email: %v", err)
	}
	return email
}

// requestID returns a 36-character value shaped like a UUIDv7 (t_usage_record
// and t_content_policy_hit both declare char(36)).
func newRequestID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("t16-%032x", binary.BigEndian.Uint64(raw[:]))
}

func seedUsage(t *testing.T, uid int64, requestID string, createdAt time.Time) {
	t.Helper()
	_, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_usage_record
			(request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
			 request_model_req, inbound_protocol, http_status, result, total_ms, created_at)
		VALUES (?,?,0,'t16-key','gpt-4o-mini','gpt-4o-mini','OPENAI_COMPAT',200,'SUCCESS',10,?)`,
		requestID, uid, createdAt)
	if err != nil {
		t.Fatalf("seed usage: %v", err)
	}
}

func seedHit(t *testing.T, uid int64, requestID, ruleID, action string, createdAt time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_content_policy_hit
			(request_id, user_id, api_key_id, model_name_snap, rule_id, category,
			 severity, action, created_at)
		VALUES (?,?,0,'gpt-4o-mini',?,'OTHER','BLOCK',?,?)`,
		requestID, uid, ruleID, action, createdAt)
	if err != nil {
		t.Fatalf("seed hit: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("hit id: %v", err)
	}
	return id
}

func hitRow(t *testing.T, id int64) (action string, appealAt sql.NullTime, appealNote sql.NullString) {
	t.Helper()
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT action, appeal_at, appeal_note FROM t_content_policy_hit WHERE id = ?`, id).
		Scan(&action, &appealAt, &appealNote); err != nil {
		t.Fatalf("hit row: %v", err)
	}
	return action, appealAt, appealNote
}

// invoke dispatches a handler directly with a synthetic session.
func invoke(t *testing.T, h http.HandlerFunc, method, target, pathID string, body any, uid int64, role string) *httptest.ResponseRecorder {
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
	if pathID != "" {
		req.SetPathValue("id", pathID)
	}
	req = req.WithContext(mw.WithSession(req.Context(), &mw.Session{UID: uid, Role: role}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func callUser(t *testing.T, h http.HandlerFunc, method, target string, body any, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	return invoke(t, h, method, target, "", body, uid, "PERSONAL")
}

func callUserID(t *testing.T, h http.HandlerFunc, method, target, pathID string, body any, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	return invoke(t, h, method, target, pathID, body, uid, "PERSONAL")
}

func callOps(t *testing.T, h http.HandlerFunc, method, target, pathID string, body any, uid int64) *httptest.ResponseRecorder {
	t.Helper()
	return invoke(t, h, method, target, pathID, body, uid, "OPS")
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := jsonx.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response (%d %s): %v", rec.Code, rec.Body.String(), err)
	}
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
	decodeBody(t, rec, &body)
	return body
}

// createTicket posts a ticket and fails the test if it is rejected.
func createTicket(t *testing.T, svc *Service, uid int64, body map[string]any) createTicketResponse {
	t.Helper()
	rec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", body, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ticket status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp createTicketResponse
	decodeBody(t, rec, &resp)
	return resp
}

func simpleTicket(typ, title string) map[string]any {
	return map[string]any{
		"ticket_type": typ,
		"title":       title,
		"description": "测试工单正文",
	}
}

func ticketState(t *testing.T, id int64) string {
	t.Helper()
	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_ticket WHERE id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("ticket state: %v", err)
	}
	return state
}

func countReplies(t *testing.T, ticketID int64) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_ticket_reply WHERE ticket_id = ?`, ticketID).Scan(&n); err != nil {
		t.Fatalf("count replies: %v", err)
	}
	return n
}

// auditRow is one TICKET audit record's fields that F21-3 requires.
type auditRow struct {
	Action     string
	OperatorID int64
	BeforeJSON string
	AfterJSON  string
	CreatedAt  time.Time
}

func auditRows(t *testing.T, ticketID int64) []auditRow {
	t.Helper()
	rows, err := testDB.QueryContext(context.Background(), `
		SELECT action, operator_id, COALESCE(before_json, ''), COALESCE(after_json, ''), created_at
		  FROM t_admin_audit_log
		 WHERE module = 'TICKET' AND target_id = ?
		 ORDER BY id ASC`, itoa(ticketID))
	if err != nil {
		t.Fatalf("audit rows: %v", err)
	}
	defer rows.Close()
	out := make([]auditRow, 0, 4)
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.Action, &r.OperatorID, &r.BeforeJSON, &r.AfterJSON, &r.CreatedAt); err != nil {
			t.Fatalf("audit scan: %v", err)
		}
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit rows err: %v", err)
	}
	return out
}
