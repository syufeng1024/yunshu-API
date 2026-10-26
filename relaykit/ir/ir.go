// Package ir is the single protocol-neutral representation used by the whole
// relay path (D-15). Every wire protocol converts to IR first and from IR
// second; there is deliberately no protocol-to-protocol shortcut.
//
// Unknown members survive in RawExtra / Extra as json.RawMessage. Dropping an
// unrecognized parameter silently disables it at the upstream, which is worse
// than forwarding it and letting the upstream reject it with a visible 400.
//
// relaykit may use encoding/json directly: it is a standalone module and
// cannot import internal/jsonx (T01 exception, limited to relaykit).
package ir

import (
	"encoding/json"
	"strings"
)

// The IR* aliases are the names used by the architecture documents and task
// cards. The short names stay canonical because internal consumers already use
// them; both names denote exactly one type, never two parallel IRs (D-15).
type (
	IRRequest  = Request
	IRResponse = Response
	IRChunk    = Chunk
	IRUsage    = Usage
)

// Request is one inbound request after protocol parsing.
type Request struct {
	Model  string
	Stream bool

	// StreamOptions carries stream_options.include_usage. A non-nil value
	// means the upstream request must send it (STREAM_OPTIONS mode).
	StreamOptions *StreamOptions

	Messages []Message
	Tools    []Tool
	// ToolChoice keeps the protocol shape verbatim; each encoder translates it
	// and each decoder stores what it saw.
	ToolChoice json.RawMessage

	Temperature    *float64
	TopP           *float64
	MaxTokens      *int64
	Stop           []string
	ResponseFormat string
	N              int
	Size           string

	// Image and embedding inputs.
	Prompt      string
	EmbedInputs []string
	ImageParams *ImageParams

	// System is the Anthropic top-level system prompt. OpenAI transports it as
	// the first system message, and the cross-protocol encoders reconcile the
	// two shapes.
	System string

	// RawExtra carries every unrecognized top-level member verbatim.
	RawExtra map[string]json.RawMessage
}

// StreamOptions mirrors stream_options.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
	Extra        map[string]json.RawMessage
}

// Message is one chat message.
type Message struct {
	Role       string
	Content    Content
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
	Extra      map[string]json.RawMessage
}

// Content is either a plain string or a list of typed parts.
type Content struct {
	Text  string
	Parts []ContentPart
}

// ContentPart is one typed content fragment.
type ContentPart struct {
	Type     string // text / image_url / image / tool_use / tool_result
	Text     string
	ImageURL string
	// MediaType is Anthropic's image media type (image/png etc.).
	MediaType string
	// ID/Name/Input describe tool_use blocks.
	ID    string
	Name  string
	Input json.RawMessage
	// ToolUseID links a tool_result to its tool_use.
	ToolUseID string
	Extra     map[string]json.RawMessage
}

// Tool is one function tool.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Extra       map[string]json.RawMessage
}

// ToolCall is one assistant-emitted function call.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ImageParams carries image-generation controls.
type ImageParams struct {
	Prompt         string
	N              int
	Size           string
	ResponseFormat string
}

// Response is one non-streaming upstream response after protocol decoding.
type Response struct {
	ID      string
	Object  string
	Model   string
	Created int64
	// StopSequence is the literal stop string that matched, when the upstream
	// reports one (Anthropic stop_sequence). OpenAI-compatible upstreams only
	// report finish_reason=stop; their field stays empty.
	StopSequence string
	Choices      []Choice
	Usage        *Usage
	Extra        map[string]json.RawMessage
}

// Chunk is one streaming frame after protocol decoding.
//
// A chunk can represent six frame kinds:
//   - content increment: HasContent == true (Delta.Content is non-empty);
//   - role announcement: Role != "" (Delta.Role carries the value too);
//   - tool-call increment: a Delta with ToolCalls;
//   - usage frame: Usage != nil;
//   - terminal frame: Done == true ([DONE] / message_stop);
//   - error frame: Error != nil.
//
// HasContent is the only first-token signal T-26 may read. Role frames,
// empty deltas and usage frames leave it false, so they cannot start the
// ttft clock by accident.
type Chunk struct {
	ID      string
	Object  string
	Model   string
	Created int64
	Choices []Choice
	Usage   *Usage
	Role    string
	// HasContent marks a frame that carries a non-empty content increment.
	HasContent bool
	// Done marks a terminal frame ([DONE] or message_stop).
	Done bool
	// Error carries an upstream error frame (Anthropic `event: error`,
	// OpenAI `data: {"error":...}`) instead of aborting with a Go error, so a
	// relay can still close the client stream correctly.
	Error *IRError
	Extra map[string]json.RawMessage
}

// IRError is an upstream failure in protocol-neutral form. UpstreamCode keeps
// the vendor code verbatim (`invalid_request_error`, `Throttling`, ...); the
// HTTP status is the one the upstream returned, or 0 when the failure never
// produced a response (DNS, TLS, timeout). Mapping to the 65 gateway codes is
// the caller's job, not relaykit's.
type IRError struct {
	UpstreamCode string
	Message      string
	HTTPStatus   int
}

// Choice is one completion choice. Message is set for non-streaming
// responses; Delta is set for streaming frames.
type Choice struct {
	Index        int
	Message      *Message
	Delta        *Delta
	FinishReason string
	Logprobs     json.RawMessage
	Extra        map[string]json.RawMessage
}

// Delta is one streaming increment.
type Delta struct {
	Role      string
	Content   string
	ToolCalls []ToolCall
	Extra     map[string]json.RawMessage
}

// Usage is the token accounting both upstream and local estimation produce.
// Every count is int64; no float32/float64 may carry tokens or money.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	// CacheReadTokens is the cached-input hit count (OpenAI
	// prompt_tokens_details.cached_tokens, Anthropic cache_read_input_tokens,
	// Bailian prompt_tokens_details.cached_tokens).
	CacheReadTokens int64
	// CacheWriteTokens is the cache-creation count (Anthropic
	// cache_creation_input_tokens); OpenAI/Bailian have no write counter.
	CacheWriteTokens int64
	// CacheHitTokens is the pre-D-15 name for CacheReadTokens. Current
	// decoders set both to the same value; new code must read CacheReadTokens.
	//
	// Deprecated: use CacheReadTokens.
	CacheHitTokens int64
	// Extra keeps unmodeled usage members verbatim (OpenAI
	// prompt_tokens_details.audio_tokens, Anthropic server_tool_use, Bailian
	// vendor counters). Nested detail members are stored as
	// "<object>.<member>", for example "prompt_tokens_details.audio_tokens".
	Extra map[string]json.RawMessage
}

// SetCacheTokens records cache-read/write counters in both fields so the
// deprecated alias cannot drift.
func (u *Usage) SetCacheTokens(read, write int64) {
	if u == nil {
		return
	}
	u.CacheReadTokens = read
	u.CacheHitTokens = read
	u.CacheWriteTokens = write
}

// Error is a terminal stream error frame. Its fields are the same six required
// fields as the HTTP error body, plus the two stream-only members.
type Error struct {
	Type              string `json:"type"`
	Code              string `json:"code"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	Hint              string `json:"hint"`
	RequestID         string `json:"request_id"`
	Partial           bool   `json:"partial"`
	CreditsConsumed   int64  `json:"credits_consumed"`
}

// InputText returns the text a local tokenizer counts for the estimate. It
// includes the system prompt and every text part.
func (r *Request) InputText() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	if r.System != "" {
		b.WriteString(r.System)
		b.WriteByte('\n')
	}
	for _, m := range r.Messages {
		if m.Content.Text != "" {
			b.WriteString(m.Content.Text)
			b.WriteByte('\n')
		}
		for _, p := range m.Content.Parts {
			if p.Type == "text" && p.Text != "" {
				b.WriteString(p.Text)
				b.WriteByte('\n')
			}
		}
	}
	if r.Prompt != "" {
		b.WriteString(r.Prompt)
		b.WriteByte('\n')
	}
	for _, in := range r.EmbedInputs {
		b.WriteString(in)
		b.WriteByte('\n')
	}
	return b.String()
}

// Clone returns a deep-enough copy for per-request mutation (for example
// injecting stream_options) without touching the Ctx copy. Slices are copied;
// RawMessage and map values are immutable by convention and shared.
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	out := *r
	if r.StreamOptions != nil {
		so := *r.StreamOptions
		out.StreamOptions = &so
	}
	if r.Messages != nil {
		out.Messages = append([]Message(nil), r.Messages...)
		for i := range out.Messages {
			if r.Messages[i].Content.Parts != nil {
				out.Messages[i].Content.Parts = append([]ContentPart(nil), r.Messages[i].Content.Parts...)
			}
			if r.Messages[i].ToolCalls != nil {
				out.Messages[i].ToolCalls = append([]ToolCall(nil), r.Messages[i].ToolCalls...)
			}
			out.Messages[i].Extra = cloneExtra(r.Messages[i].Extra)
		}
	}
	if r.Tools != nil {
		out.Tools = append([]Tool(nil), r.Tools...)
		for i := range out.Tools {
			out.Tools[i].Extra = cloneExtra(r.Tools[i].Extra)
		}
	}
	if r.Stop != nil {
		out.Stop = append([]string(nil), r.Stop...)
	}
	if r.EmbedInputs != nil {
		out.EmbedInputs = append([]string(nil), r.EmbedInputs...)
	}
	out.RawExtra = cloneExtra(r.RawExtra)
	return &out
}

func cloneExtra(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
