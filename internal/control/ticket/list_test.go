package ticket

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// F19-2: state/type filters return exactly the matching rows, and the detail
// renders the SLA as 已响应 / 剩余 xx.
func TestF19_2ListFiltersAndDetailSLA(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	svc.Mailer = &fakeMailer{}
	owner := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")

	open := createTicket(t, svc, owner, simpleTicket("TECH", "仍待处理"))
	responded := createTicket(t, svc, owner, simpleTicket("BILLING", "已回复"))
	closed := createTicket(t, svc, owner, simpleTicket("OTHER", "已关闭"))

	// Respond to the second ticket at T+30m.
	clk.Advance(30 * time.Minute)
	if rec := callOps(t, svc.Reply, http.MethodPost,
		"/api/admin/tickets/"+itoa(responded.Ticket.ID)+"/reply", itoa(responded.Ticket.ID),
		map[string]any{"content": "已收到"}, ops); rec.Code != http.StatusOK {
		t.Fatalf("reply status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Close the third at T+45m.
	clk.Advance(15 * time.Minute)
	if rec := callUserID(t, svc.Close, http.MethodPost,
		"/api/console/tickets/"+itoa(closed.Ticket.ID)+"/close",
		itoa(closed.Ticket.ID), nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("close status=%d body=%s", rec.Code, rec.Body.String())
	}
	clk.Advance(15 * time.Minute) // now = T+1h

	list := func(query string) ticketListResponse {
		t.Helper()
		rec := callUser(t, svc.List, http.MethodGet, "/api/console/tickets"+query, nil, owner)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		var resp ticketListResponse
		decodeBody(t, rec, &resp)
		return resp
	}

	openList := list("?state=OPEN")
	if openList.Total != 1 || openList.Items[0].ID != open.Ticket.ID {
		t.Fatalf("OPEN list total=%d items=%+v", openList.Total, openList.Items)
	}
	respondedList := list("?state=RESPONDED")
	if respondedList.Total != 1 || respondedList.Items[0].ID != responded.Ticket.ID {
		t.Fatalf("RESPONDED list total=%d items=%+v", respondedList.Total, respondedList.Items)
	}
	closedList := list("?state=CLOSED")
	if closedList.Total != 1 || closedList.Items[0].ID != closed.Ticket.ID {
		t.Fatalf("CLOSED list total=%d items=%+v", closedList.Total, closedList.Items)
	}
	byType := list("?type=BILLING")
	if byType.Total != 1 || byType.Items[0].ID != responded.Ticket.ID {
		t.Fatalf("type filter total=%d items=%+v", byType.Total, byType.Items)
	}

	// A fourth state is not a filter value; it is a query error.
	bad := callUser(t, svc.List, http.MethodGet, "/api/console/tickets?state=PENDING", nil, owner)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("state=PENDING status=%d, want 400", bad.Code)
	}
	if code := errorOf(t, bad).Error.Code; code != "invalid_query_parameter" {
		t.Fatalf("state=PENDING code=%q", code)
	}
	// page_size above 100 is rejected, not clamped.
	big := callUser(t, svc.List, http.MethodGet, "/api/console/tickets?page_size=101", nil, owner)
	if big.Code != http.StatusBadRequest {
		t.Fatalf("page_size=101 status=%d, want 400", big.Code)
	}

	detail := func(id int64) ticketView {
		t.Helper()
		rec := callUserID(t, svc.Detail, http.MethodGet, "/api/console/tickets/"+itoa(id), itoa(id), nil, owner)
		if rec.Code != http.StatusOK {
			t.Fatalf("detail %d status=%d body=%s", id, rec.Code, rec.Body.String())
		}
		var v ticketView
		decodeBody(t, rec, &v)
		return v
	}

	openView := detail(open.Ticket.ID)
	if openView.SLA.Responded || openView.SLA.Overdue {
		t.Fatalf("open SLA=%+v", openView.SLA)
	}
	if openView.SLA.RemainingSeconds != int64(3*time.Hour/time.Second) {
		t.Fatalf("remaining=%d, want 10800", openView.SLA.RemainingSeconds)
	}
	if openView.SLA.Display != "剩余 3 小时" {
		t.Fatalf("display=%q, want 剩余 3 小时", openView.SLA.Display)
	}

	respondedView := detail(responded.Ticket.ID)
	if !respondedView.SLA.Responded || !strings.HasPrefix(respondedView.SLA.Display, "已响应") {
		t.Fatalf("responded SLA=%+v", respondedView.SLA)
	}
	if !strings.Contains(respondedView.SLA.Display, "30 分钟") {
		t.Fatalf("display=%q, want the 30-minute response time", respondedView.SLA.Display)
	}

	closedView := detail(closed.Ticket.ID)
	if closedView.State != StateClosed {
		t.Fatalf("closed state=%s", closedView.State)
	}
	if closedView.SLA.Responded || closedView.SLA.Overdue || closedView.SLA.Display != "已关闭（未响应）" {
		t.Fatalf("closed SLA=%+v, want a stopped, non-overdue display", closedView.SLA)
	}
}

// Hard constraint 7: another account's ticket is a 404 with the same body as
// an id that never existed. Reading and closing are both covered.
func TestOtherUsersTicketIsIndistinguishableFromMissing(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")
	intruder := newTestUser(t, "PERSONAL")

	ticket := createTicket(t, svc, owner, simpleTicket("TECH", "私有工单"))
	id := itoa(ticket.Ticket.ID)

	foreignRec := callUserID(t, svc.Detail, http.MethodGet, "/api/console/tickets/"+id, id, nil, intruder)
	missingRec := callUserID(t, svc.Detail, http.MethodGet, "/api/console/tickets/999999999", "999999999", nil, intruder)
	if foreignRec.Code != http.StatusNotFound || missingRec.Code != http.StatusNotFound {
		t.Fatalf("detail statuses foreign=%d missing=%d, want 404/404", foreignRec.Code, missingRec.Code)
	}
	f, m := errorOf(t, foreignRec), errorOf(t, missingRec)
	if f.Error.Code != m.Error.Code || f.Error.Message != m.Error.Message {
		t.Fatalf("foreign/missing differ: %q/%q vs %q/%q",
			f.Error.Code, f.Error.Message, m.Error.Code, m.Error.Message)
	}
	t.Logf("他人工单详情 → HTTP %d %s %q", foreignRec.Code, f.Error.Code, f.Error.Message)
	t.Logf("不存在工单详情 → HTTP %d %s %q", missingRec.Code, m.Error.Code, m.Error.Message)

	closeRec := callUserID(t, svc.Close, http.MethodPost, "/api/console/tickets/"+id+"/close", id, nil, intruder)
	if closeRec.Code != http.StatusNotFound {
		t.Fatalf("foreign close status=%d, want 404", closeRec.Code)
	}
	if got := ticketState(t, ticket.Ticket.ID); got != StateOpen {
		t.Fatalf("a foreign close changed the state to %s", got)
	}

	list := callUser(t, svc.List, http.MethodGet, "/api/console/tickets", nil, intruder)
	var resp ticketListResponse
	decodeBody(t, list, &resp)
	for _, item := range resp.Items {
		if item.ID == ticket.Ticket.ID {
			t.Fatalf("intruder list leaked ticket %d", ticket.Ticket.ID)
		}
	}

	// A non-numeric id is the same 404, not a 400 that confirms the id shape.
	badID := callUserID(t, svc.Detail, http.MethodGet, "/api/console/tickets/abc", "abc", nil, intruder)
	if badID.Code != http.StatusNotFound {
		t.Fatalf("non-numeric id status=%d, want 404", badID.Code)
	}
}
