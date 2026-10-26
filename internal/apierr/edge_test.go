package apierr

import (
	"bytes"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/ratelimit"
)

func TestZeroValueErrorNormalizesToInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Write(rec, &APIError{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := decodeBody(t, rec.Body.Bytes())
	if body["code"] != CodeInternalError {
		t.Fatalf("code = %v, want internal_error", body["code"])
	}
}

func TestFromVerdictUsesCurrentClock(t *testing.T) {
	v := ratelimit.Verdict{
		Allowed: false, Code: ratelimit.CodeRPM,
		AllHit: []string{ratelimit.CodeRPM},
		Limit:  10, Remaining: 0,
		ResetAt: time.Now().Add(30 * time.Second).Unix(),
	}
	e := FromVerdict(v)
	if e.Code != CodeRPMLimitExceeded {
		t.Fatalf("code = %q", e.Code)
	}
	if e.RetryAfterSeconds < 1 || e.RetryAfterSeconds > 60 {
		t.Fatalf("retry = %d, want 1..60", e.RetryAfterSeconds)
	}
	if e.ResetAt == "" {
		t.Fatal("reset_at is empty")
	}
}

func TestFromResetFallsBackWhenResetIsInThePast(t *testing.T) {
	now := fixedNow()
	retry, reset := fromReset(now.Add(-time.Minute).Unix(), now, 60, 1, 60)
	if retry != 60 || reset != now.Add(60*time.Second) {
		t.Fatalf("retry/reset = %d/%s, want 60/now+60s", retry, reset)
	}
	// A reset beyond the window is clamped together with the seconds.
	retry, reset = fromReset(now.Add(2*time.Hour).Unix(), now, 60, 1, 60)
	if retry != 60 || !reset.Equal(now.Add(60*time.Second)) {
		t.Fatalf("clamped retry/reset = %d/%s, want 60/now+60s", retry, reset)
	}
}

func TestApplyRateLimitHeadersExported(t *testing.T) {
	rec := httptest.NewRecorder()
	ApplyRateLimitHeaders(rec, RateLimitSnapshot{
		RPMLimit: 300, RPMRemaining: 7, RPMResetUnix: 1700000000,
		DailyLimit: 5000, DailyRemaining: 42,
	})
	want := map[string]string{
		"X-RateLimit-Limit-Requests":     "300",
		"X-RateLimit-Remaining-Requests": "7",
		"X-RateLimit-Reset-Requests":     "1700000000",
		"X-Quota-Daily-Limit":            "5000",
		"X-Quota-Daily-Remaining":        "42",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Zero limit means "unknown": no fake headers.
	rec = httptest.NewRecorder()
	ApplyRateLimitHeaders(rec, RateLimitSnapshot{})
	if len(rec.Header()) != 0 {
		t.Fatalf("unknown snapshot wrote headers: %v", rec.Header())
	}
}

func TestWriteSSEFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	e := New(CodeUpstreamEmptyResponse).WithCreditsConsumed(3)
	if err := WriteSSEFlush(rec, e, SSEAnthropic); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("missing event line: %q", rec.Body.String())
	}
	if body := frameBody(t, rec.Body.String()); body["credits_consumed"].(float64) != 3 {
		t.Fatalf("credits_consumed = %v", body["credits_consumed"])
	}
}

func TestCauseUnwrapAndIs(t *testing.T) {
	cause := errors.New("boom")
	e := New(CodeInternalError).WithCause(cause)
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is does not reach the cause")
	}
	if !errors.Is(e, New(CodeInternalError)) {
		t.Fatal("errors.Is should match the same code")
	}
	if errors.Is(e, New(CodeConvertFailed)) {
		t.Fatal("different codes must not match")
	}
	if e.Cause() != cause {
		t.Fatal("Cause mismatch")
	}
}

func TestSanitizeTextExported(t *testing.T) {
	got := SanitizeText("connect https://10.0.0.8/v1 with Bearer abcdefghijk and sk-live-123456789 duplicate")
	for _, secret := range []string{"10.0.0.8", "abcdefghijk", "sk-live-123456789"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SanitizeText left %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "duplicate") {
		t.Fatalf("SanitizeText removed ordinary text: %q", got)
	}
}

func TestRenderHintWithoutTokenAndEmpty(t *testing.T) {
	if got := renderHint("", 5); got == "" {
		t.Fatal("empty hint template must produce a fallback")
	}
	if got := renderHint("按 {retry} 秒重试", 7); got != "按 7 秒重试" {
		t.Fatalf("renderHint = %q", got)
	}
}

func TestFormatCredits(t *testing.T) {
	cases := map[float64]string{
		0:       "0",
		50:      "50",
		1200:    "1,200",
		-12.34:  "-12.34",
		-0.5:    "-0.5",
		1234567: "1,234,567",
		12.5:    "12.5",
	}
	for in, want := range cases {
		if got := formatCredits(in); got != want {
			t.Errorf("formatCredits(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestUnknownVerdictDimensionLabel(t *testing.T) {
	if got := dimensionLabel("future_dimension"); got != "future_dimension" {
		t.Fatalf("label = %q", got)
	}
}

func TestImageURLHintKeepsSchemeText(t *testing.T) {
	_, body := writeError(t, New(CodeInvalidImageURL))
	msg := body["message"].(string)
	if !strings.Contains(msg, "https://") || !strings.Contains(msg, "data:image/") {
		t.Fatalf("scheme text was over-scrubbed: %q", msg)
	}
}

func TestMarshalFailureFallsBackToInternalError(t *testing.T) {
	e := Newf(CodeRPMLimitExceeded, int64(1), 1).WithLimit(math.NaN())
	rec := httptest.NewRecorder()
	if err := Write(rec, e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 fallback", rec.Code)
	}
	body := decodeBody(t, rec.Body.Bytes())
	if body["code"] != CodeInternalError {
		t.Fatalf("code = %v, want internal_error", body["code"])
	}
	if body["request_id"] == "" {
		t.Fatal("fallback lost the request id")
	}
}

func TestSSEDoesNotMutateOriginalError(t *testing.T) {
	e := Newf(CodeUpstreamTimeout, 1)
	var buf bytes.Buffer
	if err := WriteSSE(&buf, e, SSEAnthropic); err != nil {
		t.Fatal(err)
	}
	raw, err := e.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"partial"`) || strings.Contains(string(raw), `"credits_consumed"`) {
		t.Fatalf("SSE fields leaked into the normal body: %s", raw)
	}
}
