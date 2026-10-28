package log

import (
	"context"
	"strings"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/obs/mask"
)

// log_kind values. The two shapes never mix: a consumer can switch on this key
// without guessing which fields are present (T22 §12.2).
const (
	KindRequest = "request"
	KindAttempt = "attempt"
)

// Stage values for the request record's stage field (TDS §5.1). The value is
// the pipeline phase, not the P-number stored in fail_stage.
const (
	StageRecv      = "recv"
	StageAuth      = "auth"
	StageRateLimit = "ratelimit"
	StageRoute     = "route"
	StagePrehold   = "prehold"
	StageUpstream  = "upstream"
	StageSettle    = "settle"
	StageDone      = "done"
)

// RequestRecord is the request-level log line. Field order is the output key
// order. TS/LogKind/Svc/Env/PID are filled by the Logger; callers leave them
// empty.
//
// SpanID is empty because distributed tracing needs otel, which V1.0 does not
// introduce (D-02). DegradeFlags is always an array; the writer turns nil into
// [] so consumers never see null.
type RequestRecord struct {
	TS              string   `json:"ts"`
	LogKind         string   `json:"log_kind"`
	Svc             string   `json:"svc"`
	Env             string   `json:"env"`
	PID             int      `json:"pid"`
	TraceID         string   `json:"trace_id"`
	SpanID          string   `json:"span_id"`
	RequestID       string   `json:"request_id"`
	UserID          string   `json:"user_id"`
	KeyID           string   `json:"key_id"`
	ModelRequested  string   `json:"model_requested"`
	ModelResolved   string   `json:"model_resolved"`
	ProtocolIn      string   `json:"protocol_in"`
	ProtocolOut     string   `json:"protocol_out"`
	ChannelID       string   `json:"channel_id"`
	Stream          bool     `json:"stream"`
	BizDate         string   `json:"biz_date"`
	Stage           string   `json:"stage"`
	ErrCode         string   `json:"err_code"`
	HTTPStatus      int      `json:"http_status"`
	InTokens        int64    `json:"in_tokens"`
	OutTokens       int64    `json:"out_tokens"`
	UsageSource     string   `json:"usage_source"`
	PlatformInEst   int64    `json:"platform_in_est"`
	PlatformOutEst  int64    `json:"platform_out_est"`
	CreditsPreheld  int64    `json:"credits_preheld"`
	CreditsSettled  int64    `json:"credits_settled"`
	CreditsRefunded int64    `json:"credits_refunded"`
	AttemptCount    int64    `json:"attempt_count"`
	TTFTMS          *int64   `json:"ttft_ms"`
	LatencyMS       int64    `json:"latency_ms"`
	UpstreamMS      int64    `json:"upstream_ms"`
	ConfigVersion   int64    `json:"config_version"`
	GroupCode       string   `json:"group_code"`
	DegradeFlags    []string `json:"degrade_flags"`
}

// AttemptRecord is the attempt-level log line, one per upstream attempt.
type AttemptRecord struct {
	TS            string `json:"ts"`
	LogKind       string `json:"log_kind"`
	Svc           string `json:"svc"`
	Env           string `json:"env"`
	PID           int    `json:"pid"`
	TraceID       string `json:"trace_id"`
	SpanID        string `json:"span_id"`
	RequestID     string `json:"request_id"`
	UserID        string `json:"user_id"`
	AttemptNo     int    `json:"attempt_no"`
	ChannelID     string `json:"channel_id"`
	ChannelName   string `json:"channel_name"`
	UpstreamModel string `json:"upstream_model"`
	ProtocolOut   string `json:"protocol_out"`
	Phase         string `json:"phase"`
	Outcome       string `json:"outcome"`
	HTTPStatus    *int64 `json:"http_status"`
	UpstreamCode  string `json:"upstream_code"`
	ConnectMS     *int64 `json:"connect_ms"`
	FirstByteMS   *int64 `json:"first_byte_ms"`
	TotalMS       int64  `json:"total_ms"`
	InTokens      int64  `json:"in_tokens"`
	OutTokens     int64  `json:"out_tokens"`
	ErrorClass    string `json:"error_class"`
}

// tsMillisLayout is RFC3339 with millisecond precision (TDS §5.1).
const tsMillisLayout = "2006-01-02T15:04:05.000Z07:00"

// WriteRequest emits one request-level JSON line. Correlation fields are
// explicit in the record; ctx is only a fallback for a caller that left them
// empty.
func (l *Logger) WriteRequest(ctx context.Context, r RequestRecord) {
	if l == nil {
		return
	}
	if r.TraceID == "" || r.RequestID == "" {
		m := MetaFrom(ctx)
		if r.TraceID == "" {
			r.TraceID = m.TraceID
		}
		if r.RequestID == "" {
			r.RequestID = m.RequestID
		}
	}
	r.TS = l.timestamp()
	r.LogKind = KindRequest
	r.Svc, r.Env, r.PID = l.svc, l.env, l.pid
	r.TraceID = cleanStr(r.TraceID, 128)
	r.SpanID = cleanStr(r.SpanID, 32)
	r.RequestID = cleanStr(r.RequestID, 128)
	r.UserID = cleanStr(r.UserID, 64)
	r.KeyID = cleanStr(r.KeyID, 64)
	r.ModelRequested = cleanStr(r.ModelRequested, 256)
	r.ModelResolved = cleanStr(r.ModelResolved, 256)
	r.ProtocolIn = cleanStr(r.ProtocolIn, 32)
	r.ProtocolOut = cleanStr(r.ProtocolOut, 32)
	r.ChannelID = cleanStr(r.ChannelID, 64)
	r.BizDate = cleanStr(r.BizDate, 16)
	r.Stage = cleanStr(r.Stage, 32)
	r.ErrCode = cleanStr(r.ErrCode, 64)
	r.UsageSource = cleanStr(r.UsageSource, 32)
	r.GroupCode = cleanStr(r.GroupCode, 64)
	r.DegradeFlags = cleanStrings(r.DegradeFlags, 64)
	l.writeRecord(r)
}

// WriteAttempt emits one attempt-level JSON line.
func (l *Logger) WriteAttempt(ctx context.Context, r AttemptRecord) {
	if l == nil {
		return
	}
	if r.TraceID == "" || r.RequestID == "" {
		m := MetaFrom(ctx)
		if r.TraceID == "" {
			r.TraceID = m.TraceID
		}
		if r.RequestID == "" {
			r.RequestID = m.RequestID
		}
	}
	r.TS = l.timestamp()
	r.LogKind = KindAttempt
	r.Svc, r.Env, r.PID = l.svc, l.env, l.pid
	r.TraceID = cleanStr(r.TraceID, 128)
	r.SpanID = cleanStr(r.SpanID, 32)
	r.RequestID = cleanStr(r.RequestID, 128)
	r.UserID = cleanStr(r.UserID, 64)
	r.ChannelID = cleanStr(r.ChannelID, 64)
	r.ChannelName = cleanStr(r.ChannelName, 128)
	r.UpstreamModel = cleanStr(r.UpstreamModel, 256)
	r.ProtocolOut = cleanStr(r.ProtocolOut, 32)
	r.Phase = cleanStr(r.Phase, 32)
	r.Outcome = cleanStr(r.Outcome, 32)
	r.UpstreamCode = cleanStr(r.UpstreamCode, 64)
	r.ErrorClass = cleanStr(r.ErrorClass, 64)
	l.writeRecord(r)
}

// BizDate is the settlement and aggregation date key: the UTC+8 calendar date
// (TDS §5.1). It is the only place this conversion is implemented.
func BizDate(t time.Time) string {
	return t.UTC().Add(8 * time.Hour).Format("2006-01-02")
}

// StageOf maps a fail_stage value to the stage enum in the request log.
//
// fail_stage has two shapes in the codebase: the pipeline writes P-numbers
// (P05, P12, P08.5), while the dispatcher's attribution writes the T-35 words
// (ROUTE, UPSTREAM, CONVERT, SETTLE, CLIENT_ABORT). Both are mapped here; a
// P-number is never written to stage verbatim. An unknown non-empty value maps
// to "" rather than a fabricated phase.
func StageOf(failStage string) string {
	s := strings.ToUpper(strings.TrimSpace(failStage))
	if s == "" {
		return StageDone
	}
	switch s {
	case "RECV", "REQUEST", "P01", "P02", "P03", "P04":
		return StageRecv
	case "AUTH", "P05", "P06", "P07", "P08", "P08.5":
		return StageAuth
	case "RATELIMIT", "RATE_LIMIT", "P11":
		return StageRateLimit
	case "ROUTE", "P12", "P13":
		return StageRoute
	case "PREHOLD", "BILLING", "P09", "P10":
		return StagePrehold
	case "UPSTREAM", "CONVERT", "CLIENT_ABORT",
		"P14", "P15", "P16", "P17", "P18", "P19", "P20", "P21", "P22", "P23", "P24", "P25":
		return StageUpstream
	case "SETTLE", "P26", "P27", "P28", "P29":
		return StageSettle
	default:
		return ""
	}
}

func (l *Logger) timestamp() string {
	if l.now == nil {
		return time.Now().UTC().Format(tsMillisLayout)
	}
	return l.now.Now().UTC().Format(tsMillisLayout)
}

func (l *Logger) writeRecord(v any) {
	if l.out == nil {
		return
	}
	line, err := jsonx.Marshal(v)
	if err != nil {
		// The structs contain only strings, integers and string slices; a
		// marshal failure is not reachable. Dropping the line keeps the
		// single-line guarantee and never blocks settlement.
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(line, '\n'))
}

// cleanStr sanitises line-breaking characters, redacts credential-shaped
// substrings and caps the length.
func cleanStr(s string, max int) string {
	return mask.Redact(sanitizeText(s, max))
}

func cleanStrings(in []string, max int) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, cleanStr(s, max))
	}
	return out
}
