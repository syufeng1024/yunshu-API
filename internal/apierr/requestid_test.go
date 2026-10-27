package apierr

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// EnsureRequestID rewrites an unusable id in place, so the header and the body
// can never disagree about which request the client is quoting.
func TestEnsureRequestIDReplacesUnusableValues(t *testing.T) {
	cases := map[string]string{
		"whitespace":   "  ",
		"inner space":  "abc def",
		"control char": "abc\ndef",
		"too long":     strings.Repeat("a", maxRequestIDLen+1),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			e := Newf(CodeInternalError)
			e.RequestID = bad
			got := e.EnsureRequestID()
			if got == "" {
				t.Fatal("EnsureRequestID returned an empty id")
			}
			if got == bad {
				t.Fatalf("EnsureRequestID kept the unusable id %q", bad)
			}
			if e.RequestID != got {
				t.Fatalf("EnsureRequestID did not write back: field=%q return=%q", e.RequestID, got)
			}
		})
	}
}

// An id that is already usable must be preserved verbatim.
func TestEnsureRequestIDKeepsUsableValue(t *testing.T) {
	const id = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	e := Newf(CodeInternalError)
	e.RequestID = id
	if got := e.EnsureRequestID(); got != id {
		t.Fatalf("EnsureRequestID = %q, want the original id", got)
	}
}

// A negative remaining on a success response is the "unknown" sentinel: the
// header must be omitted rather than carrying a fake balance.
func TestSetResponseHeadersOmitsNegativeCredits(t *testing.T) {
	rec := httptest.NewRecorder()
	neg := -1.0
	rid := SetResponseHeaders(rec, ResponseMeta{CreditsRemaining: &neg})
	if rid == "" {
		t.Fatal("SetResponseHeaders must generate a request id")
	}
	if got := rec.Header().Get("X-Request-Id"); got != rid {
		t.Fatalf("X-Request-Id = %q, want %q", got, rid)
	}
	if got := rec.Header().Get("X-Credits-Remaining"); got != "" {
		t.Fatalf("X-Credits-Remaining = %q, want it omitted for the negative sentinel", got)
	}
}
