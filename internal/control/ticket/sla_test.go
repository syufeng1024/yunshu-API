package ticket

import (
	"net/http"
	"testing"
	"time"
)

// A11 / F21-2: after 4 hours with no reply the ticket is still OPEN. It enters
// the alert zone, gains an overdue SLA display, and nothing closes, replies to
// or escalates it. Time passing is the entire input to this test.
func TestA11_OverdueTicketStaysOpenAndEntersAlertZone(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	mailer := &fakeMailer{}
	svc.Mailer = mailer
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("TECH", "无人应答"))
	id := itoa(ticket.Ticket.ID)
	if !ticket.Ticket.FirstResponseDueAt.Equal(testBase.Add(firstResponseSLA)) {
		t.Fatalf("due_at=%s, want created+4h=%s",
			ticket.Ticket.FirstResponseDueAt, testBase.Add(firstResponseSLA))
	}

	// Five hours later: the SLA is breached by exactly one hour.
	clk.Advance(5 * time.Hour)

	detail := callUserID(t, svc.Detail, http.MethodGet, "/api/console/tickets/"+id, id, nil, owner)
	var view ticketView
	decodeBody(t, detail, &view)
	if view.State != StateOpen {
		t.Fatalf("state=%s after the SLA breach, want OPEN", view.State)
	}
	if !view.SLA.Overdue || view.SLA.Responded {
		t.Fatalf("sla=%+v, want overdue and not responded", view.SLA)
	}
	if view.SLA.OverdueSeconds != int64(time.Hour/time.Second) {
		t.Fatalf("overdue_seconds=%d, want 3600", view.SLA.OverdueSeconds)
	}
	if view.SLA.Display != "已超时 1 小时" {
		t.Fatalf("display=%q", view.SLA.Display)
	}

	alert := callOps(t, svc.ListAdmin, http.MethodGet, "/api/admin/tickets?overdue=true", "", nil, ops)
	if alert.Code != http.StatusOK {
		t.Fatalf("alert list status=%d body=%s", alert.Code, alert.Body.String())
	}
	var alertResp adminTicketListResponse
	decodeBody(t, alert, &alertResp)
	if alertResp.Total != 1 || len(alertResp.Items) != 1 || alertResp.Items[0].ID != ticket.Ticket.ID {
		t.Fatalf("alert total=%d items=%+v, want the overdue ticket", alertResp.Total, alertResp.Items)
	}
	if !alertResp.Items[0].SLA.Overdue {
		t.Fatalf("alert item SLA=%+v", alertResp.Items[0].SLA)
	}

	// The breach changed display fields only. No reply, no mail, no closure.
	if got := ticketState(t, ticket.Ticket.ID); got != StateOpen {
		t.Fatalf("state=%s, want OPEN (no automatic close)", got)
	}
	if v := firstRespondedAt(t, ticket.Ticket.ID); v.Valid {
		t.Fatalf("first_responded_at=%v, want NULL (no automatic reply)", v)
	}
	if n := countReplies(t, ticket.Ticket.ID); n != 0 {
		t.Fatalf("replies=%d, want 0 (no automatic reply)", n)
	}
	if mailer.count() != 0 {
		t.Fatalf("mails=%d, want 0", mailer.count())
	}

	t.Logf("假时钟 +5h：state=%s sla.overdue=%v display=%q 告警区条数=%d 回复数=%d 邮件数=%d",
		view.State, view.SLA.Overdue, view.SLA.Display, alertResp.Total,
		countReplies(t, ticket.Ticket.ID), mailer.count())

	// Another 48 hours: still OPEN. There is no timer that eventually closes.
	clk.Advance(48 * time.Hour)
	if got := ticketState(t, ticket.Ticket.ID); got != StateOpen {
		t.Fatalf("state=%s after 53h, want OPEN", got)
	}
	t.Logf("假时钟再 +48h（累计 53h）：state=%s 仍未自动关闭", ticketState(t, ticket.Ticket.ID))
}

// The alert zone orders by how long each ticket has been overdue (oldest due
// first), and overdue=false is its exact complement.
func TestSLAAlertZoneOrderingAndComplement(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	older := createTicket(t, svc, owner, simpleTicket("TECH", "更早超时"))
	clk.Advance(time.Hour)
	newer := createTicket(t, svc, owner, simpleTicket("TECH", "较晚超时"))
	clk.Advance(10 * time.Hour) // both are now overdue
	onTime := createTicket(t, svc, owner, simpleTicket("TECH", "还在时限内"))

	call := func(query string) adminTicketListResponse {
		t.Helper()
		rec := callOps(t, svc.ListAdmin, http.MethodGet, "/api/admin/tickets"+query, "", nil, ops)
		if rec.Code != http.StatusOK {
			t.Fatalf("admin list %s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		var resp adminTicketListResponse
		decodeBody(t, rec, &resp)
		return resp
	}

	alert := call("?overdue=true")
	if alert.Total != 2 || len(alert.Items) != 2 {
		t.Fatalf("alert total=%d items=%+v", alert.Total, alert.Items)
	}
	if alert.Items[0].ID != older.Ticket.ID || alert.Items[1].ID != newer.Ticket.ID {
		t.Fatalf("alert order=%d,%d, want oldest-overdue first (%d,%d)",
			alert.Items[0].ID, alert.Items[1].ID, older.Ticket.ID, newer.Ticket.ID)
	}

	rest := call("?overdue=false")
	if rest.Total != 1 || rest.Items[0].ID != onTime.Ticket.ID {
		t.Fatalf("overdue=false total=%d items=%+v", rest.Total, rest.Items)
	}

	bad := callOps(t, svc.ListAdmin, http.MethodGet, "/api/admin/tickets?overdue=maybe", "", nil, ops)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("overdue=maybe status=%d, want 400", bad.Code)
	}
}
