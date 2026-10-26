// Package log writes the single-line JSON log format defined for all three
// processes. The key set is fixed: callers cannot attach arbitrary fields, so
// credentials and request bodies cannot leak through this package.
package log

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

// Level is a log severity.
type Level string

// Log levels.
const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Meta is the request-scoped correlation data carried in a context.
type Meta struct {
	TraceID   string
	RequestID string
}

type metaKey struct{}

// WithMeta stores request correlation data in ctx.
func WithMeta(ctx context.Context, m Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFrom reads request correlation data from ctx.
func MetaFrom(ctx context.Context) Meta {
	if ctx == nil {
		return Meta{}
	}
	m, _ := ctx.Value(metaKey{}).(Meta)
	return m
}

// Fields are the per-record values. Unset string fields are rendered as "".
type Fields struct {
	TraceID   string
	RequestID string
	UID       string
	KID       string
	Model     string
	Stage     string
	LatencyMS int64
	Status    int
}

// Logger writes a fixed-schema JSON line per call.
type Logger struct {
	svc string
	env string
	pid int
	out io.Writer
	now clock.Clock
	mu  sync.Mutex
}

// New returns a logger writing to stdout.
func New(svc, env string) *Logger {
	return NewWithWriter(svc, env, os.Stdout, clock.Real{})
}

// NewWithWriter returns a logger writing to out, using c as its clock.
func NewWithWriter(svc, env string, out io.Writer, c clock.Clock) *Logger {
	if c == nil {
		c = clock.Real{}
	}
	return &Logger{
		svc: sanitizeText(svc, 64),
		env: sanitizeText(env, 32),
		pid: os.Getpid(),
		out: out,
		now: c,
	}
}

// Info writes an info-level record.
func (l *Logger) Info(ctx context.Context, msg string, f Fields) {
	l.write(ctx, LevelInfo, msg, f)
}

// Warn writes a warn-level record.
func (l *Logger) Warn(ctx context.Context, msg string, f Fields) {
	l.write(ctx, LevelWarn, msg, f)
}

// Error writes an error-level record.
func (l *Logger) Error(ctx context.Context, msg string, f Fields) {
	l.write(ctx, LevelError, msg, f)
}

// record deliberately has no omitempty tags: every key is present on every line.
type record struct {
	TS        string `json:"ts"`
	Level     string `json:"level"`
	Svc       string `json:"svc"`
	Env       string `json:"env"`
	PID       int    `json:"pid"`
	TraceID   string `json:"trace_id"`
	RequestID string `json:"request_id"`
	UID       string `json:"uid"`
	KID       string `json:"kid"`
	Model     string `json:"model"`
	Stage     string `json:"stage"`
	LatencyMS int64  `json:"latency_ms"`
	Status    int    `json:"status"`
	Msg       string `json:"msg"`
}

func (l *Logger) write(ctx context.Context, level Level, msg string, f Fields) {
	m := MetaFrom(ctx)
	if f.TraceID == "" {
		f.TraceID = m.TraceID
	}
	if f.RequestID == "" {
		f.RequestID = m.RequestID
	}

	rec := record{
		TS:        l.now.Now().UTC().Format(time.RFC3339Nano),
		Level:     string(level),
		Svc:       l.svc,
		Env:       l.env,
		PID:       l.pid,
		TraceID:   sanitizeText(f.TraceID, 128),
		RequestID: sanitizeText(f.RequestID, 128),
		UID:       sanitizeText(f.UID, 128),
		KID:       sanitizeText(f.KID, 128),
		Model:     sanitizeText(f.Model, 256),
		Stage:     sanitizeText(f.Stage, 64),
		LatencyMS: f.LatencyMS,
		Status:    f.Status,
		Msg:       sanitizeText(msg, 2048),
	}

	line, err := jsonx.Marshal(rec)
	if err != nil {
		// A record made of strings and integers cannot fail to marshal. Keep the
		// schema and the single-line guarantee even if that assumption breaks.
		line = []byte(`{"ts":"` + rec.TS + `","level":"error","svc":"","env":"","pid":0,` +
			`"trace_id":"","request_id":"","uid":"","kid":"","model":"","stage":"",` +
			`"latency_ms":0,"status":0,"msg":"log marshal failed"}`)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(line, '\n'))
}

// sanitizeText strips control characters (so a header or upstream message
// cannot forge extra lines) and caps the value length.
func sanitizeText(s string, max int) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// drop other control characters
		default:
			b.WriteRune(r)
		}
		if b.Len() >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// String returns the level as its wire value.
func (l Level) String() string { return string(l) }
