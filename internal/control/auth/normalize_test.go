package auth

import "testing"

func TestNormalizeIdentifierEmail(t *testing.T) {
	got, err := NormalizeIdentifier("  Local＠Yunshu.Dev  ")
	if err != nil {
		t.Fatalf("NormalizeIdentifier: %v", err)
	}
	if got.Kind != KindEmail || got.Value != "local@yunshu.dev" {
		t.Fatalf("got %+v, want lower-case email", got)
	}
}

func TestNormalizeIdentifierPhone(t *testing.T) {
	got, err := NormalizeIdentifier(" +86 (138) 0013-8000 ")
	if err != nil {
		t.Fatalf("NormalizeIdentifier: %v", err)
	}
	if got.Kind != KindPhone || got.Value != "8613800138000" {
		t.Fatalf("got %+v", got)
	}
}

func TestNormalizeIdentifierRejects(t *testing.T) {
	for _, raw := range []string{"", "   ", "@example.com", "a@b", "a@@b.com", "12345", "13800138000x"} {
		if _, err := NormalizeIdentifier(raw); err == nil {
			t.Fatalf("%q was accepted", raw)
		}
	}
}

func TestMaskIdentifier(t *testing.T) {
	if got := maskIdentifier(KindEmail, "alice@example.com"); got != "a***@example.com" {
		t.Fatalf("email mask = %q", got)
	}
	if got := maskIdentifier(KindPhone, "8613800138000"); got != "861****00" {
		t.Fatalf("phone mask = %q", got)
	}
}
