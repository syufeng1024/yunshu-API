package mw

import (
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

func testManager(t *testing.T) *SessionManager {
	t.Helper()
	return &SessionManager{
		Secret: []byte("0123456789abcdef"),
		TTL:    SessionTTL,
		Now:    clock.Fixed{},
	}
}

func TestCookieRoundTrip(t *testing.T) {
	m := testManager(t)
	issued := time.Unix(1_700_000_000, 0).UTC()
	value := m.cookieValue("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", issued)

	sid, gotIssued, ok := m.parseCookie(value)
	if !ok {
		t.Fatal("cookie did not parse")
	}
	if sid != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("sid = %q", sid)
	}
	if !gotIssued.Equal(issued) {
		t.Fatalf("issued = %s, want %s", gotIssued, issued)
	}
}

func TestCookieTamperingRejected(t *testing.T) {
	m := testManager(t)
	issued := time.Unix(1_700_000_000, 0).UTC()
	sid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	value := m.cookieValue(sid, issued)

	// Change the issued-at: the HMAC covers it, so the absolute-cap bypass
	// attempt fails.
	tampered := sid + "." + "1700000001" + "." + value[len(sid)+len(".1700000000."):]
	if _, _, ok := m.parseCookie(tampered); ok {
		t.Fatal("tampered cookie was accepted")
	}
	if _, _, ok := m.parseCookie("not-a-cookie"); ok {
		t.Fatal("malformed cookie was accepted")
	}
}

func TestCookieSignatureIsSecretBound(t *testing.T) {
	m := testManager(t)
	other := testManager(t)
	other.Secret = []byte("fedcba9876543210")
	issued := time.Unix(1_700_000_000, 0).UTC()
	sid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, _, ok := other.parseCookie(m.cookieValue(sid, issued)); ok {
		t.Fatal("a cookie signed with another secret was accepted")
	}
}
