package log

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

func TestLoggerWritesFixedSchemaInUTC(t *testing.T) {
	var buf bytes.Buffer
	cst := time.FixedZone("CST", 8*3600)
	c := clock.NewFixed(time.Date(2026, 10, 26, 12, 0, 0, 123456789, cst))
	l := NewWithWriter("gatewayd", "dev", &buf, c)

	ctx := WithMeta(t.Context(), Meta{TraceID: "0123456789abcdef0123456789abcdef", RequestID: "req-1"})
	l.Info(ctx, "hello\nworld", Fields{
		UID:       "u-1",
		KID:       "k-1",
		Model:     "qwen-max",
		Stage:     "auth",
		LatencyMS: 12,
		Status:    200,
	})

	line := strings.TrimSuffix(buf.String(), "\n")
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("log line must not contain a newline: %q", line)
	}

	var got map[string]any
	if err := jsonx.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, line)
	}

	wantKeys := []string{
		"ts", "level", "svc", "env", "pid", "trace_id", "request_id",
		"uid", "kid", "model", "stage", "latency_ms", "status", "msg",
	}
	if len(got) != len(wantKeys) {
		t.Fatalf("want exactly %d keys, got %d: %v", len(wantKeys), len(got), got)
	}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing fixed key %q", k)
		}
	}

	if got["ts"] != "2026-10-26T04:00:00.123456789Z" {
		t.Errorf("ts = %v, want UTC RFC3339Nano", got["ts"])
	}
	if got["level"] != "info" || got["svc"] != "gatewayd" || got["env"] != "dev" {
		t.Errorf("base fields wrong: %v", got)
	}
	if got["trace_id"] != "0123456789abcdef0123456789abcdef" || got["request_id"] != "req-1" {
		t.Errorf("correlation fields wrong: %v", got)
	}
	if got["uid"] != "u-1" || got["kid"] != "k-1" || got["model"] != "qwen-max" || got["stage"] != "auth" {
		t.Errorf("request fields wrong: %v", got)
	}
	if got["latency_ms"] != float64(12) || got["status"] != float64(200) {
		t.Errorf("numeric fields wrong: %v", got)
	}
	if got["msg"] != "hello world" {
		t.Errorf("msg = %q, want newline sanitized", got["msg"])
	}
}

func TestLoggerKeepsUnsetKeys(t *testing.T) {
	var buf bytes.Buffer
	l := NewWithWriter("controld", "", &buf, clock.NewFixed(time.Unix(0, 0)))
	l.Warn(t.Context(), "only msg", Fields{})

	var got map[string]any
	if err := jsonx.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"trace_id", "request_id", "uid", "kid", "model", "stage"} {
		v, ok := got[k]
		if !ok {
			t.Fatalf("key %q omitted", k)
		}
		if v != "" {
			t.Errorf("key %q = %v, want empty string", k, v)
		}
	}
	if got["latency_ms"] != float64(0) || got["status"] != float64(0) {
		t.Errorf("numeric defaults wrong: %v", got)
	}
}
