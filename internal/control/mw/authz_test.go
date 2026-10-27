package mw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// T15: the matrix is keyed by the PRD §6.3 four-role enum. This table states
// each cell explicitly instead of deriving it from rolePermissions, so a typo
// in the matrix fails here rather than agreeing with itself.
func TestRolePermissionMatrix(t *testing.T) {
	all := []Permission{
		PermUserRead, PermUserWrite, PermKeyRead, PermKeyWrite,
		PermModelRead, PermModelWrite, PermPriceWrite, PermGroupWrite,
		PermChannelWrite, PermNoticeWrite, PermRiskWrite, PermConfigWrite,
		PermFundRead, PermFundWrite, PermFundApprove,
		PermTicketRead, PermTicketWrite, PermTicketReply,
		PermAuditRead, PermLogRead, PermUsageRead, PermAdminRead, PermAdminAll,
	}
	want := map[string]map[Permission]bool{
		"PERSONAL": {
			PermUserRead: true, PermKeyRead: true, PermKeyWrite: true,
			PermModelRead: true, PermFundRead: true, PermFundWrite: true,
			PermTicketRead: true, PermTicketWrite: true, PermLogRead: true,
			PermUsageRead: true,
		},
		"OPS": {
			PermUserRead: true, PermUserWrite: true, PermModelRead: true,
			PermModelWrite: true, PermPriceWrite: true, PermGroupWrite: true,
			PermChannelWrite: true, PermNoticeWrite: true, PermRiskWrite: true,
			PermConfigWrite: true, PermTicketReply: true, PermAuditRead: true,
			PermAdminRead: true,
		},
		"FINANCE": {
			PermFundRead: true, PermFundApprove: true, PermAuditRead: true,
			PermModelRead: true, PermAdminRead: true,
		},
	}
	for _, p := range all {
		want["SUPER_ADMIN"] = setOn(want["SUPER_ADMIN"], p)
	}

	for _, role := range []string{"PERSONAL", "OPS", "FINANCE", "SUPER_ADMIN"} {
		for _, p := range all {
			if got := Allows(role, p); got != want[role][p] {
				t.Errorf("Allows(%q, %q) = %v, want %v", role, p, got, want[role][p])
			}
		}
	}

	// The separation that protects money: OPS cannot approve fund movements,
	// FINANCE cannot change prices or channels.
	if Allows("OPS", PermFundApprove) || Allows("OPS", PermFundWrite) {
		t.Error("OPS must not hold fund.approve / fund.write")
	}
	if Allows("FINANCE", PermPriceWrite) || Allows("FINANCE", PermChannelWrite) {
		t.Error("FINANCE must not hold price.write / channel.write")
	}
	if Allows("OPS", PermAdminAll) || Allows("FINANCE", PermAdminAll) {
		t.Error("admin.all is SUPER_ADMIN only; concrete permissions must guard admin routes")
	}
	// The ticket consoles are split: OPS answers users through ticket.reply but
	// must not read or write the account-scoped console surface; PERSONAL must
	// not reach the operator queue.
	if Allows("OPS", PermTicketRead) || Allows("OPS", PermTicketWrite) {
		t.Error("OPS holds ticket.reply only; ticket.read/ticket.write belong to the account console")
	}
	if Allows("PERSONAL", PermTicketReply) {
		t.Error("PERSONAL must not hold ticket.reply")
	}
	if Allows("FINANCE", PermTicketRead) || Allows("FINANCE", PermTicketWrite) || Allows("FINANCE", PermTicketReply) {
		t.Error("FINANCE has no ticket permission")
	}
	// log.read is account-scoped. No operator role may hold it, otherwise an
	// operator could open a customer's request log through the console route.
	if Allows("OPS", PermLogRead) || Allows("FINANCE", PermLogRead) {
		t.Error("log.read must stay with PERSONAL and SUPER_ADMIN only")
	}
	// usage.read has the same account scope as log.read: the operator
	// consoles have no usage route, so no operator role may hold it.
	if Allows("OPS", PermUsageRead) || Allows("FINANCE", PermUsageRead) {
		t.Error("usage.read must stay with PERSONAL and SUPER_ADMIN only")
	}

	// An unknown role grants nothing.
	for _, p := range all {
		if Allows("", p) || Allows("NOT_A_ROLE", p) {
			t.Errorf("unknown role granted %q", p)
		}
	}
}

// setOn is a test helper: mark one permission true in a fresh map copy.
func setOn(m map[Permission]bool, p Permission) map[Permission]bool {
	if m == nil {
		m = make(map[Permission]bool)
	}
	m[p] = true
	return m
}

func TestRequireWithoutSessionIs401(t *testing.T) {
	h := Require(PermAdminAll)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler must not run without a session")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/admin/whoami", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireWrongRoleIs403(t *testing.T) {
	h := Require(PermAdminAll)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler must not run for a PERSONAL account")
	}))
	r := httptest.NewRequest("GET", "/api/admin/whoami", nil)
	r = r.WithContext(WithSession(r.Context(), &Session{UID: 1, Role: "PERSONAL"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRequireAllowedRole(t *testing.T) {
	called := false
	h := Require(PermAdminAll)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest("GET", "/api/admin/whoami", nil)
	r = r.WithContext(WithSession(r.Context(), &Session{UID: 1, Role: "SUPER_ADMIN"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("called=%v status=%d", called, rec.Code)
	}
}
