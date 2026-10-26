package mw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRolePermissionMatrix(t *testing.T) {
	cases := []struct {
		role string
		perm Permission
		want bool
	}{
		{"PERSONAL", PermModelRead, true},
		{"PERSONAL", PermKeyWrite, true},
		{"PERSONAL", PermAdminAll, false},
		{"OPS", PermAdminAll, true},
		{"OPS", PermKeyWrite, false},
		{"FINANCE", PermAdminAll, true},
		{"SUPER_ADMIN", PermAdminAll, true},
		{"SUPER_ADMIN", PermKeyWrite, true},
		{"", PermAdminAll, false},
		{"NOT_A_ROLE", PermModelRead, false},
	}
	for _, c := range cases {
		if got := Allows(c.role, c.perm); got != c.want {
			t.Fatalf("Allows(%q, %q) = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
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
	r = r.WithContext(WithSession(r.Context(), &Session{UID: 1, Role: "OPS"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("called=%v status=%d", called, rec.Code)
	}
}
