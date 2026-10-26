package apierr

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"yunshu.dev/core/internal/ratelimit"
)

// TestReceiptEvidence prints the A5 / A6 / A9 / A8 artifacts used in the T10
// completion receipt. It asserts the same invariants so the log cannot drift
// from the tests.
func TestReceiptEvidence(t *testing.T) {
	t.Run("A3-65-codes", func(t *testing.T) {
		t.Logf("codes=%d registry=%d\n%s", len(Codes()), len(Specs()), MarkdownTable())
	})

	t.Run("A5-402", func(t *testing.T) {
		e := NewInsufficientCredits(1860, -12.34, 50)
		rec, body := writeError(t, e)
		t.Logf("status=%d headers={X-Credits-Remaining:%q Retry-After:%q} body=%s",
			rec.Code, rec.Header().Get("X-Credits-Remaining"), rec.Header().Get("Retry-After"),
			rec.Body.String())
		if body["remaining"].(float64) != -12.34 {
			t.Fatal("remaining mismatch")
		}
	})

	t.Run("A6-429", func(t *testing.T) {
		now := time.Date(2026, 10, 23, 0, 0, 0, 0, shanghai)
		next := time.Date(2026, 10, 24, 0, 0, 0, 0, shanghai)
		e := FromVerdictAt(ratelimit.Verdict{
			Allowed: false, Code: ratelimit.CodeConcurrency,
			AllHit: []string{
				ratelimit.CodeConcurrency, ratelimit.CodeRPM,
				ratelimit.CodeTPM, ratelimit.CodeDailyQuota,
			},
			Limit: 5000, Total: 5000, Remaining: 0,
			ResetAt: next.Unix(), ResetAtMax: next.Unix(),
		}, now).WithRateLimitSnapshot(RateLimitSnapshot{
			RPMLimit: 300, RPMRemaining: 0, RPMResetUnix: next.Unix(),
			DailyLimit: 5000, DailyRemaining: 0,
		})
		rec, _ := writeError(t, e)
		t.Logf("status=%d code=%s retry=%d limit=%v remaining=%v reset_at=%s message=%q",
			rec.Code, e.Code, e.RetryAfterSeconds, *e.Limit, *e.Remaining, e.ResetAt, e.Message)
		t.Logf("body=%s", rec.Body.String())
		t.Logf("headers={Retry-After:%q X-RateLimit-Limit-Requests:%q X-RateLimit-Remaining-Requests:%q X-RateLimit-Reset-Requests:%q X-Quota-Daily-Limit:%q X-Quota-Daily-Remaining:%q}",
			rec.Header().Get("Retry-After"),
			rec.Header().Get("X-RateLimit-Limit-Requests"),
			rec.Header().Get("X-RateLimit-Remaining-Requests"),
			rec.Header().Get("X-RateLimit-Reset-Requests"),
			rec.Header().Get("X-Quota-Daily-Limit"),
			rec.Header().Get("X-Quota-Daily-Remaining"))
	})

	t.Run("A8-request-id", func(t *testing.T) {
		rec, body := writeError(t, New(CodeInvalidAPIKey))
		t.Logf("header=%q body=%q equal=%v",
			rec.Header().Get("X-Request-Id"), body["request_id"],
			rec.Header().Get("X-Request-Id") == body["request_id"])
	})

	t.Run("A9-SSE", func(t *testing.T) {
		e := Newf(CodeUpstreamTimeout, 1).WithRequestID("01J8X2K4M6P8Q0R2S4T6V8W0X8").WithCreditsConsumed(42)
		consumed, preheld, remaining := int64(42), int64(100), 950.0
		rec := httptest.NewRecorder()
		SetResponseHeaders(rec, ResponseMeta{
			RequestID: e.RequestID, Stream: true,
			CreditsConsumed: &consumed, CreditsPreheld: &preheld, CreditsRemaining: &remaining,
		})
		var buf bytes.Buffer
		if err := WriteSSE(&buf, e, SSEAnthropic); err != nil {
			t.Fatal(err)
		}
		if got := rec.Header().Get("X-Credits-Consumed"); got != "" {
			t.Fatalf("stream emitted X-Credits-Consumed=%q", got)
		}
		t.Logf("headers={X-Request-Id:%q X-Credits-Preheld:%q X-Credits-Remaining:%q X-Credits-Consumed:%q Cache-Control:%q X-Accel-Buffering:%q}",
			rec.Header().Get("X-Request-Id"), rec.Header().Get("X-Credits-Preheld"),
			rec.Header().Get("X-Credits-Remaining"), rec.Header().Get("X-Credits-Consumed"),
			rec.Header().Get("Cache-Control"), rec.Header().Get("X-Accel-Buffering"))
		t.Logf("frame=%q", buf.String())
	})
}
