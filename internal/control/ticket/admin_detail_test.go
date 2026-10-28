package ticket

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

// A3: an operator reads another user's ticket, sees the description and both
// visible replies and internal notes, and the read changes no state/audit.
func TestAdminTicketDetailIncludesInternalReplies(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "PERSONAL")
	ops := newTestUser(t, "OPS")
	created := createTicket(t, svc, applicant, simpleTicket("TECH", "详情工单"))
	ticketID := created.Ticket.ID
	id := strconv.FormatInt(ticketID, 10)

	rec := callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": "公开回复", "is_internal": false}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("public reply status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = callOps(t, svc.Reply, http.MethodPost, "/api/admin/tickets/"+id+"/reply", id,
		map[string]any{"content": "内部备注", "is_internal": true}, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("internal note status=%d body=%s", rec.Code, rec.Body.String())
	}

	beforeState := ticketState(t, ticketID)
	var beforeAudit int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE module = 'TICKET' AND target_id = ?`, id).Scan(&beforeAudit); err != nil {
		t.Fatalf("count audit before detail: %v", err)
	}

	rec = callOps(t, svc.DetailAdmin, http.MethodGet, "/api/admin/tickets/"+id, id, nil, ops)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail adminTicketDetailView
	decodeBody(t, rec, &detail)

	if detail.Description != "测试工单正文" {
		t.Fatalf("description=%q, want 测试工单正文", detail.Description)
	}
	if detail.UserID != applicant || detail.UserEmail != userEmail(t, applicant) {
		t.Fatalf("user=%d/%q, want %d/%q", detail.UserID, detail.UserEmail, applicant, userEmail(t, applicant))
	}
	if len(detail.Replies) != 2 {
		t.Fatalf("replies=%d, want 2 (visible + internal)", len(detail.Replies))
	}
	if detail.Replies[0].Content != "公开回复" || detail.Replies[0].IsInternal {
		t.Fatalf("first reply=%+v, want a visible public reply", detail.Replies[0])
	}
	if detail.Replies[1].Content != "内部备注" || !detail.Replies[1].IsInternal {
		t.Fatalf("second reply=%+v, want an internal note", detail.Replies[1])
	}

	if afterState := ticketState(t, ticketID); afterState != beforeState {
		t.Fatalf("detail changed state %q -> %q", beforeState, afterState)
	}
	var afterAudit int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE module = 'TICKET' AND target_id = ?`, id).Scan(&afterAudit); err != nil {
		t.Fatalf("count audit after detail: %v", err)
	}
	if afterAudit != beforeAudit {
		t.Fatalf("detail wrote %d audit rows", afterAudit-beforeAudit)
	}
}

// A4: an absent row and a malformed path id both answer resource_not_found.
// The route's ticket.reply permission is the authorization boundary; a caller
// without it never reaches this handler (403 at middleware).
func TestAdminTicketDetailNotFound(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	ops := newTestUser(t, "OPS")

	for _, rawID := range []string{"999999999", "not-a-number"} {
		rec := callOps(t, svc.DetailAdmin, http.MethodGet,
			"/api/admin/tickets/"+rawID, rawID, nil, ops)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("id %q status=%d body=%s, want 404", rawID, rec.Code, rec.Body.String())
		}
		if code := errorOf(t, rec).Error.Code; code != "resource_not_found" {
			t.Fatalf("id %q code=%q, want resource_not_found", rawID, code)
		}
	}
}
