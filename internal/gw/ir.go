package gw

import (
	"strings"

	"yunshu.dev/core/internal/jsonx"
)

// IRRequest is the protocol-neutral request (D-11 two-stage conversion). Only
// the fields the inbound half needs are populated here; P14 (T08) consumes the
// same struct for outbound conversion.
type IRRequest struct {
	Model  string
	Stream bool

	Messages   []IRMessage
	Tools      []IRTool
	ToolChoice jsonx.RawMessage

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
	ImageParams *IRImageParams

	// System is the Anthropic top-level system prompt.
	System string

	// RawExtra carries every unrecognized top-level member verbatim so the
	// upstream request does not silently lose parameters (TDS §3.2 P14).
	RawExtra map[string]jsonx.RawMessage
}

// IRMessage is one chat message.
type IRMessage struct {
	Role    string
	Content IRContent
	Name    string
}

// IRContent is either a plain string or a list of typed parts.
type IRContent struct {
	Text  string
	Parts []IRContentPart
}

// IRContentPart is one typed content fragment.
type IRContentPart struct {
	Type     string // text / image_url / tool_use / tool_result
	Text     string
	ImageURL string
}

// IRTool is one function tool.
type IRTool struct {
	Name        string
	Description string
	Parameters  jsonx.RawMessage
}

// IRImageParams carries the image-generation controls.
type IRImageParams struct {
	Prompt         string
	N              int
	Size           string
	ResponseFormat string
}

// InputText returns the text the local tokenizer counts for the estimate. It
// includes the system prompt and every text part, joined with newlines.
func (r *IRRequest) InputText() string {
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
