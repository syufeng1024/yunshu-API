package apierr

import (
	"strings"
	"testing"
)

const (
	validTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	validTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
)

func TestParseTraceparent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"valid", validTraceparent, validTraceID, true},
		{"leading space", " " + validTraceparent, validTraceID, true},
		{"uppercase hex is not W3C-legal", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01", "", false},
		{"all-zero trace id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "", false},
		{"wrong version", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "", false},
		{"trace id short", "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01", "", false},
		{"trace id non-hex", "00-4bf92f3577b34da6a3ce929d0e0e473z-00f067aa0ba902b7-01", "", false},
		{"span id short", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b-01", "", false},
		{"flags non-hex", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-zz", "", false},
		{"too few parts", "00-4bf92f3577b34da6a3ce929d0e0e4736-01", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseTraceparent(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ParseTraceparent(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// A-附加1: the four T22 §12.4 rows.
func TestResolveRequestIDFourCases(t *testing.T) {
	t.Run("legal traceparent without X-Request-Id", func(t *testing.T) {
		if got := ResolveRequestID("", validTraceparent); got != validTraceID {
			t.Fatalf("ResolveRequestID = %q, want traceparent trace id %q", got, validTraceID)
		}
	})

	t.Run("client X-Request-Id wins over traceparent", func(t *testing.T) {
		const client = "client-rid-42"
		if got := ResolveRequestID(client, validTraceparent); got != client {
			t.Fatalf("ResolveRequestID = %q, want client value %q", got, client)
		}
	})

	t.Run("neither generates an id", func(t *testing.T) {
		got := ResolveRequestID("", "")
		if got == "" {
			t.Fatal("ResolveRequestID generated an empty id")
		}
		if got == validTraceID {
			t.Fatal("ResolveRequestID reused the trace id without a traceparent")
		}
	})

	t.Run("invalid traceparent is ignored", func(t *testing.T) {
		got := ResolveRequestID("", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		if got == "" {
			t.Fatal("ResolveRequestID generated an empty id")
		}
		if got == validTraceID {
			t.Fatal("an invalid traceparent must not supply the trace id")
		}
	})

	t.Run("unsafe X-Request-Id is not echoed", func(t *testing.T) {
		got := ResolveRequestID("abc\r\nX-Evil: 1", "")
		if strings.ContainsAny(got, "\r\n") || got == "abc\r\nX-Evil: 1" {
			t.Fatalf("ResolveRequestID echoed an unsafe header value %q", got)
		}
	})
}
