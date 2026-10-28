package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

var requestKeys = []string{
	"ts", "log_kind", "svc", "env", "pid", "trace_id", "span_id",
	"request_id", "user_id", "key_id", "model_requested", "model_resolved",
	"protocol_in", "protocol_out", "channel_id", "stream", "biz_date",
	"stage", "err_code", "http_status", "in_tokens", "out_tokens",
	"usage_source", "platform_in_est", "platform_out_est", "credits_preheld",
	"credits_settled", "credits_refunded", "attempt_count", "ttft_ms",
	"latency_ms", "upstream_ms", "config_version", "group_code", "degrade_flags",
}

var attemptKeys = []string{
	"ts", "log_kind", "svc", "env", "pid", "trace_id", "span_id",
	"request_id", "user_id", "attempt_no", "channel_id", "channel_name",
	"upstream_model", "protocol_out", "phase", "outcome", "http_status",
	"upstream_code", "connect_ms", "first_byte_ms", "total_ms", "in_tokens",
	"out_tokens", "error_class",
}

func newTestLogger(buf *bytes.Buffer) *Logger {
	return NewWithWriter("gatewayd", "test", buf, clock.NewFixed(time.Date(2026, 10, 28, 4, 5, 6, 789000000, time.UTC)))
}

func fullRequestRecord() RequestRecord {
	ttft := int64(120)
	return RequestRecord{
		TraceID:         "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:          "",
		RequestID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		UserID:          "42",
		KeyID:           "7",
		ModelRequested:  "gpt-4o",
		ModelResolved:   "gpt-4o-2024-08-06",
		ProtocolIn:      "openai",
		ProtocolOut:     "dashscope",
		ChannelID:       "10",
		Stream:          true,
		BizDate:         "2026-10-28",
		Stage:           StageDone,
		ErrCode:         "",
		HTTPStatus:      200,
		InTokens:        11,
		OutTokens:       22,
		UsageSource:     "upstream",
		PlatformInEst:   10,
		PlatformOutEst:  20,
		CreditsPreheld:  1000,
		CreditsSettled:  900,
		CreditsRefunded: 100,
		AttemptCount:    1,
		TTFTMS:          &ttft,
		LatencyMS:       321,
		UpstreamMS:      300,
		ConfigVersion:   7,
		GroupCode:       "standard",
	}
}

func fullAttemptRecord() AttemptRecord {
	status, connect, first := int64(200), int64(12), int64(30)
	return AttemptRecord{
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:        "",
		RequestID:     "4bf92f3577b34da6a3ce929d0e0e4736",
		UserID:        "42",
		AttemptNo:     1,
		ChannelID:     "10",
		ChannelName:   "ch-east",
		UpstreamModel: "qwen-max",
		ProtocolOut:   "dashscope",
		Phase:         "P17",
		Outcome:       "SUCCESS",
		HTTPStatus:    &status,
		UpstreamCode:  "",
		ConnectMS:     &connect,
		FirstByteMS:   &first,
		TotalMS:       300,
		InTokens:      11,
		OutTokens:     22,
		ErrorClass:    "",
	}
}

// orderedKeys returns the top-level object keys in wire order.
func orderedKeys(t *testing.T, line string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	if _, err := dec.Token(); err != nil { // opening '{'
		t.Fatalf("decode opening brace: %v (line=%q)", err, line)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("decode key: %v", err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("object key is not a string: %v", tok)
		}
		keys = append(keys, key)
		var value any
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("decode value for %q: %v", key, err)
		}
	}
	return keys
}

func assertKeys(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("key count = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

// A10: the two record schemas are fixed, ordered, and contain no verbatim
// credential even when a caller passes user-controlled text.
func TestA10RequestRecordSchema(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)
	l.WriteRequest(t.Context(), fullRequestRecord())

	line := strings.TrimSpace(buf.String())
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("log line must be a single line: %q", line)
	}
	assertKeys(t, orderedKeys(t, line), requestKeys)

	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	flags, ok := got["degrade_flags"].([]any)
	if !ok || len(flags) != 0 {
		t.Fatalf("degrade_flags = %v, want []", got["degrade_flags"])
	}
	if got["log_kind"] != KindRequest || got["svc"] != "gatewayd" || got["env"] != "test" {
		t.Fatalf("logger-filled fields wrong: %v", got)
	}
	if got["ts"] != "2026-10-28T04:05:06.789Z" {
		t.Fatalf("ts = %v, want RFC3339 milliseconds in UTC", got["ts"])
	}
	if got["ttft_ms"] != float64(120) {
		t.Fatalf("ttft_ms = %v, want 120", got["ttft_ms"])
	}
}

func TestA10AttemptRecordSchema(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)
	l.WriteAttempt(t.Context(), fullAttemptRecord())

	line := strings.TrimSpace(buf.String())
	assertKeys(t, orderedKeys(t, line), attemptKeys)

	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["log_kind"] != KindAttempt {
		t.Fatalf("log_kind = %v, want %q", got["log_kind"], KindAttempt)
	}
	if got["attempt_no"] != float64(1) || got["http_status"] != float64(200) {
		t.Fatalf("attempt fields wrong: %v", got)
	}
}

func TestA10NilPointerRendersNull(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	req := fullRequestRecord()
	req.TTFTMS = nil
	l.WriteRequest(t.Context(), req)
	if line := strings.TrimSpace(buf.String()); !strings.Contains(line, `"ttft_ms":null`) {
		t.Fatalf("nil ttft_ms must render as null, got %q", line)
	}

	buf.Reset()
	att := fullAttemptRecord()
	att.HTTPStatus, att.ConnectMS, att.FirstByteMS = nil, nil, nil
	l.WriteAttempt(t.Context(), att)
	line := strings.TrimSpace(buf.String())
	for _, key := range []string{`"http_status":null`, `"connect_ms":null`, `"first_byte_ms":null`} {
		if !strings.Contains(line, key) {
			t.Fatalf("missing %s in %q", key, line)
		}
	}
}

func TestA10CredentialsNeverReachTheLine(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf)

	req := fullRequestRecord()
	req.ModelRequested = "Authorization: Bearer sk-abc123"
	l.WriteRequest(t.Context(), req)
	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "sk-abc123") {
		t.Fatalf("raw credential leaked into the log line: %q", line)
	}
	if strings.Contains(line, "Bearer sk-abc123") {
		t.Fatalf("raw Authorization value leaked into the log line: %q", line)
	}
}
