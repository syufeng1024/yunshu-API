package ticket

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

func firstRespondedAt(t *testing.T, ticketID int64) sql.NullTime {
	t.Helper()
	var v sql.NullTime
	if err := testDB.QueryRowContext(t.Context(),
		`SELECT first_responded_at FROM t_ticket WHERE id = ?`, ticketID).Scan(&v); err != nil {
		t.Fatalf("first_responded_at: %v", err)
	}
	return v
}

func lastRepliedAt(t *testing.T, ticketID int64) sql.NullTime {
	t.Helper()
	var v sql.NullTime
	if err := testDB.QueryRowContext(t.Context(),
		`SELECT last_replied_at FROM t_ticket WHERE id = ?`, ticketID).Scan(&v); err != nil {
		t.Fatalf("last_replied_at: %v", err)
	}
	return v
}

type auditAfter struct {
	State           string `json:"state"`
	IsInternal      bool   `json:"is_internal"`
	HasInternalNote bool   `json:"has_internal_note"`
}

func decodeAfter(t *testing.T, raw string) auditAfter {
	t.Helper()
	var v auditAfter
	if err := jsonx.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode audit after %q: %v", raw, err)
	}
	return v
}

func decodeBeforeState(t *testing.T, raw string) string {
	t.Helper()
	var v struct {
		State string `json:"state"`
	}
	if err := jsonx.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode audit before %q: %v", raw, err)
	}
	return v.State
}

// F21-1: a public reply moves the ticket to RESPONDED, stamps the first
// response only once, and sends exactly one e-mail per public reply.
func TestF21_1ReplySetsRespondedAndSendsMail(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	mailer := &fakeMailer{}
	svc.Mailer = mailer
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("BILLING", "扣费异常"))

	clk.Advance(time.Hour)
	rec := callOps(t, svc.Reply, http.MethodPost,
		"/api/admin/tickets/"+itoa(ticket.Ticket.ID)+"/reply", itoa(ticket.Ticket.ID),
		map[string]any{"content": "已核实，积分将在 24 小时内补回"}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("reply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp adminReplyResponse
	decodeBody(t, rec, &resp)
	if resp.Ticket.State != StateResponded {
		t.Fatalf("state=%s, want RESPONDED", resp.Ticket.State)
	}
	if got := ticketState(t, ticket.Ticket.ID); got != StateResponded {
		t.Fatalf("db state=%s, want RESPONDED", got)
	}
	first := firstRespondedAt(t, ticket.Ticket.ID)
	if !first.Valid || !first.Time.UTC().Equal(testBase.Add(time.Hour)) {
		t.Fatalf("first_responded_at=%v, want %s", first, testBase.Add(time.Hour))
	}
	if mailer.count() != 1 {
		t.Fatalf("mails=%d, want 1", mailer.count())
	}
	if got := mailer.last().To; got != userEmail(t, owner) {
		t.Fatalf("mail to=%q, want %q", got, userEmail(t, owner))
	}
	if !strings.Contains(mailer.last().Subject, ticket.Ticket.TicketNo) {
		t.Fatalf("mail subject=%q, want the ticket number", mailer.last().Subject)
	}

	// A second reply must not move the first-response stopwatch.
	clk.Advance(2 * time.Hour)
	rec = callOps(t, svc.Reply, http.MethodPost,
		"/api/admin/tickets/"+itoa(ticket.Ticket.ID)+"/reply", itoa(ticket.Ticket.ID),
		map[string]any{"content": "补充说明"}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("second reply status=%d body=%s", rec.Code, rec.Body.String())
	}
	again := firstRespondedAt(t, ticket.Ticket.ID)
	if !again.Valid || !again.Time.UTC().Equal(first.Time.UTC()) {
		t.Fatalf("first_responded_at moved: %v -> %v", first, again)
	}
	last := lastRepliedAt(t, ticket.Ticket.ID)
	if !last.Valid || !last.Time.UTC().Equal(testBase.Add(3*time.Hour)) {
		t.Fatalf("last_replied_at=%v, want %s", last, testBase.Add(3*time.Hour))
	}
	if mailer.count() != 2 {
		t.Fatalf("mails=%d, want 2", mailer.count())
	}
	t.Logf("F21-1：state=%s first_responded_at=%s（第二次回复未改动）邮件数=%d 收件人=%s",
		resp.Ticket.State, again.Time.UTC().Format(time.RFC3339), mailer.count(), mailer.last().To)

	detail := callUserID(t, svc.Detail, http.MethodGet,
		"/api/console/tickets/"+itoa(ticket.Ticket.ID), itoa(ticket.Ticket.ID), nil, owner)
	var view ticketView
	decodeBody(t, detail, &view)
	if len(view.Replies) != 2 || view.Replies[0].Content != "已核实，积分将在 24 小时内补回" {
		t.Fatalf("user replies=%+v", view.Replies)
	}
	if !view.SLA.Responded || !strings.HasPrefix(view.SLA.Display, "已响应") {
		t.Fatalf("sla=%+v, want 已响应", view.SLA)
	}
}

// Hard constraint 6: an internal note is invisible to the account, does not
// stop the SLA clock, sends no mail, and changes no state.
func TestInternalNoteIsInvisibleAndDoesNotStopSLA(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	mailer := &fakeMailer{}
	svc.Mailer = mailer
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("TECH", "接口超时"))
	clk.Advance(30 * time.Minute)

	rec := callOps(t, svc.Reply, http.MethodPost,
		"/api/admin/tickets/"+itoa(ticket.Ticket.ID)+"/reply", itoa(ticket.Ticket.ID),
		map[string]any{"content": "内部：该用户曾多次重复提交", "is_internal": true}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("internal note status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := ticketState(t, ticket.Ticket.ID); got != StateOpen {
		t.Fatalf("state=%s, want OPEN after an internal note", got)
	}
	if v := firstRespondedAt(t, ticket.Ticket.ID); v.Valid {
		t.Fatalf("first_responded_at=%v, want NULL: an internal note is not a response", v)
	}
	if mailer.count() != 0 {
		t.Fatalf("mails=%d, want 0 for an internal note", mailer.count())
	}

	// A later public reply becomes the visible first response.
	clk.Advance(30 * time.Minute)
	rec = callOps(t, svc.Reply, http.MethodPost,
		"/api/admin/tickets/"+itoa(ticket.Ticket.ID)+"/reply", itoa(ticket.Ticket.ID),
		map[string]any{"content": "已定位到上游抖动"}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("public reply status=%d", rec.Code)
	}
	if v := firstRespondedAt(t, ticket.Ticket.ID); !v.Valid || !v.Time.UTC().Equal(testBase.Add(time.Hour)) {
		t.Fatalf("first_responded_at=%v, want T+1h (the public reply)", v)
	}

	detail := callUserID(t, svc.Detail, http.MethodGet,
		"/api/console/tickets/"+itoa(ticket.Ticket.ID), itoa(ticket.Ticket.ID), nil, owner)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	if strings.Contains(detail.Body.String(), "内部：该用户曾多次重复提交") {
		t.Fatalf("user detail leaked the internal note: %s", detail.Body.String())
	}
	var view ticketView
	decodeBody(t, detail, &view)
	if len(view.Replies) != 1 || view.Replies[0].Content != "已定位到上游抖动" {
		t.Fatalf("visible replies=%+v, want only the public reply", view.Replies)
	}
	t.Logf("运营写入内部备注：%q", "内部：该用户曾多次重复提交")
	t.Logf("用户拉详情：state=%s 可见回复数=%d 内容=%q 含内部备注=%v",
		view.State, len(view.Replies), view.Replies[0].Content,
		strings.Contains(detail.Body.String(), "内部：该用户曾多次重复提交"))

	// Operator audit still records the note as its own action.
	rows := auditRows(t, ticket.Ticket.ID)
	if len(rows) != 2 || rows[0].Action != auditActionInternalNote || rows[1].Action != auditActionReply {
		t.Fatalf("audit actions=%v, want INTERNAL_NOTE then REPLY", actionsOf(rows))
	}
	if a := decodeAfter(t, rows[0].AfterJSON); !a.HasInternalNote || !a.IsInternal {
		t.Fatalf("internal-note audit after=%+v", a)
	}
}

// F21-3: reply + internal note + state change produce three separate audit
// rows, each with operator, time, before/after state and the note flag.
func TestF21_3ThreeOperatorActionsWriteThreeAuditRows(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	svc.Mailer = &fakeMailer{}
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("OTHER", "咨询"))
	path := "/api/admin/tickets/" + itoa(ticket.Ticket.ID)

	if rec := callOps(t, svc.Reply, http.MethodPost, path+"/reply", itoa(ticket.Ticket.ID),
		map[string]any{"content": "您好，已收到"}, ops); rec.Code != http.StatusOK {
		t.Fatalf("reply status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := callOps(t, svc.Reply, http.MethodPost, path+"/reply", itoa(ticket.Ticket.ID),
		map[string]any{"content": "内部：与工单 T-9 相关", "is_internal": true}, ops); rec.Code != http.StatusOK {
		t.Fatalf("note status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := callOps(t, svc.State, http.MethodPost, path+"/state", itoa(ticket.Ticket.ID),
		map[string]any{"state": "CLOSED", "reason": "用户已确认解决"}, ops); rec.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", rec.Code, rec.Body.String())
	}

	rows := auditRows(t, ticket.Ticket.ID)
	if len(rows) != 3 {
		t.Fatalf("audit rows=%d (%v), want exactly 3", len(rows), actionsOf(rows))
	}
	want := []struct {
		action, before, after string
		internal, note        bool
	}{
		{auditActionReply, StateOpen, StateResponded, false, false},
		{auditActionInternalNote, StateResponded, StateResponded, true, true},
		{auditActionStateChange, StateResponded, StateClosed, false, false},
	}
	for i, w := range want {
		r := rows[i]
		if r.Action != w.action {
			t.Fatalf("row %d action=%q, want %q", i, r.Action, w.action)
		}
		if r.OperatorID != ops {
			t.Fatalf("row %d operator=%d, want %d", i, r.OperatorID, ops)
		}
		if r.CreatedAt.IsZero() {
			t.Fatalf("row %d has no timestamp", i)
		}
		if got := decodeBeforeState(t, r.BeforeJSON); got != w.before {
			t.Fatalf("row %d before=%q, want %q", i, got, w.before)
		}
		a := decodeAfter(t, r.AfterJSON)
		if a.State != w.after || a.IsInternal != w.internal || a.HasInternalNote != w.note {
			t.Fatalf("row %d after=%+v, want state=%s internal=%v note=%v",
				i, a, w.after, w.internal, w.note)
		}
		t.Logf("审计 %d: action=%s operator_id=%d at=%s before=%s after=%s",
			i+1, r.Action, r.OperatorID, r.CreatedAt.Format(time.RFC3339),
			r.BeforeJSON, r.AfterJSON)
	}
	if got := ticketState(t, ticket.Ticket.ID); got != StateClosed {
		t.Fatalf("state=%s, want CLOSED", got)
	}
}

func actionsOf(rows []auditRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

// Hard constraint 9: once CLOSED, neither a public reply nor an internal note
// is accepted.
func TestClosedTicketRejectsReplies(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	svc.Mailer = &fakeMailer{}
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("TECH", "已解决"))
	id := itoa(ticket.Ticket.ID)
	if rec := callUserID(t, svc.Close, http.MethodPost, "/api/console/tickets/"+id+"/close", id, nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("close status=%d body=%s", rec.Code, rec.Body.String())
	}

	reply := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": "迟到回复"}, ops)
	if reply.Code != http.StatusConflict {
		t.Fatalf("reply to CLOSED status=%d, want 409", reply.Code)
	}
	if code := errorOf(t, reply).Error.Code; code != "resource_state_conflict" {
		t.Fatalf("reply to CLOSED code=%q", code)
	}
	note := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": "内部备注", "is_internal": true}, ops)
	if note.Code != http.StatusConflict {
		t.Fatalf("note on CLOSED status=%d, want 409", note.Code)
	}
	if n := countReplies(t, ticket.Ticket.ID); n != 0 {
		t.Fatalf("replies=%d, want 0 after the rejections", n)
	}
}

func TestReplyValidationAndMissingTicket(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("TECH", "校验"))
	id := itoa(ticket.Ticket.ID)

	if rec := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": "   "}, ops); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty content status=%d, want 400", rec.Code)
	}
	if rec := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": strings.Repeat("字", maxReplyRunes+1)}, ops); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized content status=%d, want 400", rec.Code)
	}
	if rec := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/999999999/reply", "999999999",
		map[string]any{"content": "不存在"}, ops); rec.Code != http.StatusNotFound {
		t.Fatalf("missing ticket status=%d, want 404", rec.Code)
	}

	// No mailer injected: the reply still succeeds (WARN only).
	svc.Mailer = nil
	if rec := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": "无邮件通道"}, ops); rec.Code != http.StatusOK {
		t.Fatalf("reply without mailer status=%d body=%s", rec.Code, rec.Body.String())
	}
}
