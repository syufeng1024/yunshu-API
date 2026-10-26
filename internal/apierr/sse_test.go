package apierr

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func frameBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	_, data, ok := strings.Cut(raw, "data: ")
	if !ok {
		t.Fatalf("frame has no data line: %q", raw)
	}
	data = strings.TrimSuffix(strings.TrimSpace(data), "\n\n")
	var envelope map[string]map[string]any
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		t.Fatalf("decode frame data: %v (%q)", err, data)
	}
	return envelope["error"]
}

// A9: the SSE error frame carries partial=true and credits_consumed, uses the
// same six required fields as the HTTP body, and appends no [DONE].
func TestSSEErrorFrameCarriesCreditsConsumed(t *testing.T) {
	e := Newf(CodeUpstreamTimeout, 1).WithRequestID("01J8X2K4M6P8Q0R2S4T6V8W0X8").WithCreditsConsumed(42)
	var buf bytes.Buffer
	if err := WriteSSE(&buf, e, SSEAnthropic); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.HasPrefix(raw, "event: error\n") {
		t.Fatalf("Anthropic frame missing event line: %q", raw)
	}
	if strings.Contains(raw, "[DONE]") {
		t.Fatalf("error frame must not append [DONE]: %q", raw)
	}
	if strings.Contains(raw, "settlement") {
		t.Fatalf("error frame must not add a settlement frame: %q", raw)
	}
	body := frameBody(t, raw)
	for _, key := range []string{"type", "code", "message", "retry_after_seconds", "hint", "request_id", "partial", "credits_consumed"} {
		if _, ok := body[key]; !ok {
			t.Errorf("SSE frame missing %q: %v", key, body)
		}
	}
	if body["partial"] != true {
		t.Fatalf("partial = %v, want true", body["partial"])
	}
	if body["credits_consumed"].(float64) != 42 {
		t.Fatalf("credits_consumed = %v, want 42", body["credits_consumed"])
	}
	if body["request_id"] != "01J8X2K4M6P8Q0R2S4T6V8W0X8" {
		t.Fatalf("request_id = %v", body["request_id"])
	}
}

func TestSSEOpenAIHasNoEventLine(t *testing.T) {
	e := New(CodeUpstreamEmptyResponse)
	var buf bytes.Buffer
	if err := WriteSSE(&buf, e, SSEOpenAI); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "event:") {
		t.Fatalf("OpenAI frame must not carry an event line: %q", buf.String())
	}
	if !strings.HasPrefix(buf.String(), "data: ") {
		t.Fatalf("OpenAI frame shape: %q", buf.String())
	}
	body := frameBody(t, buf.String())
	if body["credits_consumed"].(float64) != 0 {
		t.Fatalf("unset credits_consumed must default to 0, got %v", body["credits_consumed"])
	}
}

func TestSSEUnknownProtocolIsRejected(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSSE(&buf, New(CodeInternalError), SSEProtocol("grpc")); err != ErrUnknownSSEProtocol {
		t.Fatalf("err = %v, want ErrUnknownSSEProtocol", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("rejected protocol wrote bytes: %q", buf.String())
	}
}

// A9 (headers): streaming responses never expose X-Credits-Consumed, even
// when the caller mistakenly supplies it.
func TestStreamHeadersNeverIncludeConsumed(t *testing.T) {
	consumed, preheld, remaining := int64(42), int64(100), 950.0
	rec := httptest.NewRecorder()
	rid := SetResponseHeaders(rec, ResponseMeta{
		RequestID:        "01J8X2K4M6P8Q0R2S4T6V8W0X9",
		Stream:           true,
		CreditsConsumed:  &consumed,
		CreditsPreheld:   &preheld,
		CreditsRemaining: &remaining,
	})
	h := rec.Header()
	if got := h.Get("X-Credits-Consumed"); got != "" {
		t.Fatalf("stream must never send X-Credits-Consumed, got %q", got)
	}
	if got := h.Get("X-Credits-Preheld"); got != "100" {
		t.Fatalf("X-Credits-Preheld = %q, want 100", got)
	}
	if got := h.Get("X-Credits-Remaining"); got != "950" {
		t.Fatalf("X-Credits-Remaining = %q, want 950", got)
	}
	if got := h.Get("X-Request-Id"); got != rid || rid != "01J8X2K4M6P8Q0R2S4T6V8W0X9" {
		t.Fatalf("request id header = %q, returned %q", got, rid)
	}
	if got := h.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := h.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
}

func TestNonStreamHeadersIncludeConsumed(t *testing.T) {
	consumed, remaining := int64(42), 950.0
	rec := httptest.NewRecorder()
	SetResponseHeaders(rec, ResponseMeta{
		RequestID:        "rid",
		CreditsConsumed:  &consumed,
		CreditsRemaining: &remaining,
	})
	if got := rec.Header().Get("X-Credits-Consumed"); got != "42" {
		t.Fatalf("X-Credits-Consumed = %q, want 42", got)
	}
	if got := rec.Header().Get("X-Credits-Remaining"); got != "950" {
		t.Fatalf("X-Credits-Remaining = %q, want 950", got)
	}
	// A negative remaining is the "unknown" sentinel on the success path.
	unknown := -1.0
	rec = httptest.NewRecorder()
	SetResponseHeaders(rec, ResponseMeta{RequestID: "rid", CreditsRemaining: &unknown})
	if got := rec.Header().Get("X-Credits-Remaining"); got != "" {
		t.Fatalf("unknown sentinel must be skipped, got %q", got)
	}
}
