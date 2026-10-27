package ticket

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCloseIsOwnerOrOperatorOnly(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	svc.Mailer = &fakeMailer{}
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	ticket := createTicket(t, svc, owner, simpleTicket("OTHER", "关闭测试"))
	id := itoa(ticket.Ticket.ID)

	// The operator closes on the user's behalf via the state endpoint.
	rec := callOps(t, svc.State, http.MethodPost, "/api/admin/tickets/"+id+"/state", id,
		map[string]any{"state": "CLOSED", "reason": "重复工单"}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator close status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := ticketState(t, ticket.Ticket.ID); got != StateClosed {
		t.Fatalf("state=%s, want CLOSED", got)
	}

	// Closing twice is a conflict, not a no-op: the caller cannot tell it
	// apart from an action that had no effect.
	againOps := callOps(t, svc.State, http.MethodPost, "/api/admin/tickets/"+id+"/state", id,
		map[string]any{"state": "CLOSED"}, ops)
	if againOps.Code != http.StatusConflict {
		t.Fatalf("second operator close status=%d, want 409", againOps.Code)
	}
	againUser := callUserID(t, svc.Close, http.MethodPost, "/api/console/tickets/"+id+"/close", id, nil, owner)
	if againUser.Code != http.StatusConflict {
		t.Fatalf("second user close status=%d, want 409", againUser.Code)
	}

	// RESPONDED cannot be set by hand: that would stop the SLA clock with no
	// reply on record.
	second := createTicket(t, svc, owner, simpleTicket("TECH", "手工置态"))
	badTarget := callOps(t, svc.State, http.MethodPost,
		"/api/admin/tickets/"+itoa(second.Ticket.ID)+"/state", itoa(second.Ticket.ID),
		map[string]any{"state": "RESPONDED"}, ops)
	if badTarget.Code != http.StatusBadRequest {
		t.Fatalf("state=RESPONDED status=%d, want 400", badTarget.Code)
	}
	missing := callOps(t, svc.State, http.MethodPost, "/api/admin/tickets/999999999/state", "999999999",
		map[string]any{"state": "CLOSED"}, ops)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing ticket status=%d, want 404", missing.Code)
	}
}

// F19-4 / PRD §10.3 ②: OPEN and RESPONDED tickets block account cancellation
// and are listed by number; CLOSED tickets do not.
func TestF19_4CancellationListsOutstandingTicketNumbers(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	svc.Mailer = &fakeMailer{}
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	open := createTicket(t, svc, owner, simpleTicket("TECH", "未结一"))
	responded := createTicket(t, svc, owner, simpleTicket("BILLING", "未结二"))
	closed := createTicket(t, svc, owner, simpleTicket("OTHER", "已了结"))

	if rec := callOps(t, svc.Reply, http.MethodPost,
		"/api/admin/tickets/"+itoa(responded.Ticket.ID)+"/reply", itoa(responded.Ticket.ID),
		map[string]any{"content": "已回复"}, ops); rec.Code != http.StatusOK {
		t.Fatalf("reply status=%d", rec.Code)
	}
	if rec := callUserID(t, svc.Close, http.MethodPost,
		"/api/console/tickets/"+itoa(closed.Ticket.ID)+"/close",
		itoa(closed.Ticket.ID), nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("close status=%d", rec.Code)
	}

	ctx := context.Background()
	err := svc.CheckAccountCancellable(ctx, owner)
	if err == nil {
		t.Fatal("cancellation allowed with OPEN/RESPONDED tickets")
	}
	var out *OutstandingTicketsError
	if !errors.As(err, &out) {
		t.Fatalf("error type %T, want *OutstandingTicketsError", err)
	}
	want := []string{open.Ticket.TicketNo, responded.Ticket.TicketNo}
	if len(out.TicketNos) != 2 || out.TicketNos[0] != want[0] || out.TicketNos[1] != want[1] {
		t.Fatalf("ticket list=%v, want %v", out.TicketNos, want)
	}
	msg := err.Error()
	if !strings.Contains(msg, open.Ticket.TicketNo) || !strings.Contains(msg, responded.Ticket.TicketNo) {
		t.Fatalf("error message %q does not list both ticket numbers", msg)
	}
	if strings.Contains(msg, closed.Ticket.TicketNo) {
		t.Fatalf("error message %q lists the CLOSED ticket", msg)
	}
	t.Logf("注销前校验：%s", msg)
	t.Logf("未结工单号=%v（CLOSED 的 %s 不计入）", out.TicketNos, closed.Ticket.TicketNo)

	// Closing both outstanding tickets unblocks cancellation.
	for _, id := range []int64{open.Ticket.ID, responded.Ticket.ID} {
		if rec := callUserID(t, svc.Close, http.MethodPost,
			"/api/console/tickets/"+itoa(id)+"/close", itoa(id), nil, owner); rec.Code != http.StatusOK {
			t.Fatalf("close %d status=%d body=%s", id, rec.Code, rec.Body.String())
		}
	}
	if err := svc.CheckAccountCancellable(ctx, owner); err != nil {
		t.Fatalf("cancellation still blocked: %v", err)
	}
	nos, err := svc.OutstandingTickets(ctx, owner)
	if err != nil || len(nos) != 0 {
		t.Fatalf("OutstandingTickets=%v err=%v, want empty", nos, err)
	}
}

// A brand-new account with no tickets is cancellable; the check is read-only
// and does not create anything.
func TestCheckAccountCancellableForEmptyAccount(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	uid := newTestUser(t, "PERSONAL")
	if err := svc.CheckAccountCancellable(context.Background(), uid); err != nil {
		t.Fatalf("fresh account blocked: %v", err)
	}
}
