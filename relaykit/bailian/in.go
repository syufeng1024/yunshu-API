// Package bailian adapts Alibaba Cloud Bailian (DashScope compatibility mode)
// as an upstream access mode. Bailian is never an outward protocol (AD-12).
//
// The package depends only on ir. It deliberately does not import the openai
// adapter even though the wire dialect is close: importing a sibling adapter
// would be a protocol-to-protocol shortcut and a second copy of the mapping.
package bailian

import (
	"encoding/json"
	"fmt"
	"strings"

	"yunshu.dev/relaykit/ir"
)

// Wire shapes for DashScope compatibility mode. Extra keeps vendor extensions
// such as enable_search, search_options and request_id that IR does not model.

type bailianRequest struct {
	Model         string                     `json:"model"`
	Messages      []bailianMessage           `json:"messages"`
	Stream        bool                       `json:"stream,omitempty"`
	StreamOptions *bailianStreamOptions      `json:"stream_options,omitempty"`
	MaxTokens     *int64                     `json:"max_tokens,omitempty"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	Stop          json.RawMessage            `json:"stop,omitempty"`
	Tools         []bailianTool              `json:"tools,omitempty"`
	ToolChoice    json.RawMessage            `json:"tool_choice,omitempty"`
	ResponseFmt   json.RawMessage            `json:"response_format,omitempty"`
	N             int                        `json:"n,omitempty"`
	Extra         map[string]json.RawMessage `json:"-"`
}

type bailianStreamOptions struct {
	IncludeUsage bool                       `json:"include_usage"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type bailianMessage struct {
	Role       string                     `json:"role"`
	Content    json.RawMessage            `json:"content,omitempty"`
	Name       string                     `json:"name,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	ToolCalls  []bailianToolCall          `json:"tool_calls,omitempty"`
	Extra      map[string]json.RawMessage `json:"-"`
}

type bailianContentPart struct {
	Type     string                     `json:"type"`
	Text     string                     `json:"text,omitempty"`
	ImageURL *bailianImageURL           `json:"image_url,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type bailianImageURL struct {
	URL   string                     `json:"url"`
	Extra map[string]json.RawMessage `json:"-"`
}

type bailianToolCall struct {
	ID       string                     `json:"id,omitempty"`
	Type     string                     `json:"type,omitempty"`
	Index    *int                       `json:"index,omitempty"`
	Function bailianToolFunctionCall    `json:"function"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type bailianToolFunctionCall struct {
	Name      string                     `json:"name,omitempty"`
	Arguments string                     `json:"arguments,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type bailianTool struct {
	Type     string                     `json:"type,omitempty"`
	Function bailianToolFunction        `json:"function"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type bailianToolFunction struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  json.RawMessage            `json:"parameters,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

type bailianResponse struct {
	ID      string                     `json:"id"`
	Object  string                     `json:"object"`
	Model   string                     `json:"model"`
	Created int64                      `json:"created"`
	Choices []bailianChoice            `json:"choices"`
	Usage   *bailianUsage              `json:"usage,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type bailianChoice struct {
	Index        int                        `json:"index"`
	Message      *bailianMessage            `json:"message,omitempty"`
	Delta        *bailianDelta              `json:"delta,omitempty"`
	FinishReason any                        `json:"finish_reason"`
	Logprobs     json.RawMessage            `json:"logprobs,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type bailianDelta struct {
	Role      string                     `json:"role,omitempty"`
	Content   string                     `json:"content,omitempty"`
	ToolCalls []bailianToolCall          `json:"tool_calls,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type bailianUsage struct {
	PromptTokens            int64                      `json:"prompt_tokens"`
	CompletionTokens        int64                      `json:"completion_tokens"`
	TotalTokens             int64                      `json:"total_tokens"`
	InputTokens             int64                      `json:"input_tokens,omitempty"`
	OutputTokens            int64                      `json:"output_tokens,omitempty"`
	PromptTokensDetails     *bailianPromptDetails      `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *bailianPromptDetails      `json:"completion_tokens_details,omitempty"`
	Extra                   map[string]json.RawMessage `json:"-"`
}

type bailianPromptDetails struct {
	CachedTokens int64                      `json:"cached_tokens,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

var (
	requestKnown          = knownSet("model", "messages", "stream", "stream_options", "max_tokens", "temperature", "top_p", "stop", "tools", "tool_choice", "response_format", "n")
	streamOptionsKnown    = knownSet("include_usage")
	messageKnown          = knownSet("role", "content", "name", "tool_call_id", "tool_calls")
	contentPartKnown      = knownSet("type", "text", "image_url")
	imageURLKnown         = knownSet("url")
	toolCallKnown         = knownSet("id", "type", "index", "function")
	toolFunctionCallKnown = knownSet("name", "arguments")
	toolKnown             = knownSet("type", "function")
	toolFunctionKnown     = knownSet("name", "description", "parameters")
	responseKnown         = knownSet("id", "object", "model", "created", "choices", "usage")
	choiceKnown           = knownSet("index", "message", "delta", "finish_reason", "logprobs")
	deltaKnown            = knownSet("role", "content", "tool_calls")
	usageKnown            = knownSet("prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens", "prompt_tokens_details", "completion_tokens_details")
	promptDetailsKnown    = knownSet("cached_tokens")
)

func (v *bailianRequest) UnmarshalJSON(data []byte) error {
	type plain bailianRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, requestKnown)
	if err != nil {
		return err
	}
	*v = bailianRequest(p)
	v.Extra = extra
	return nil
}

func (v *bailianStreamOptions) UnmarshalJSON(data []byte) error {
	type plain bailianStreamOptions
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, streamOptionsKnown)
	if err != nil {
		return err
	}
	*v = bailianStreamOptions(p)
	v.Extra = extra
	return nil
}

func (v *bailianMessage) UnmarshalJSON(data []byte) error {
	type plain bailianMessage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, messageKnown)
	if err != nil {
		return err
	}
	*v = bailianMessage(p)
	v.Extra = extra
	return nil
}

func (v *bailianContentPart) UnmarshalJSON(data []byte) error {
	type plain bailianContentPart
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, contentPartKnown)
	if err != nil {
		return err
	}
	*v = bailianContentPart(p)
	v.Extra = extra
	return nil
}

func (v *bailianImageURL) UnmarshalJSON(data []byte) error {
	type plain bailianImageURL
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, imageURLKnown)
	if err != nil {
		return err
	}
	*v = bailianImageURL(p)
	v.Extra = extra
	return nil
}

func (v *bailianToolCall) UnmarshalJSON(data []byte) error {
	type plain bailianToolCall
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolCallKnown)
	if err != nil {
		return err
	}
	*v = bailianToolCall(p)
	v.Extra = extra
	return nil
}

func (v *bailianToolFunctionCall) UnmarshalJSON(data []byte) error {
	type plain bailianToolFunctionCall
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolFunctionCallKnown)
	if err != nil {
		return err
	}
	*v = bailianToolFunctionCall(p)
	v.Extra = extra
	return nil
}

func (v *bailianTool) UnmarshalJSON(data []byte) error {
	type plain bailianTool
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolKnown)
	if err != nil {
		return err
	}
	*v = bailianTool(p)
	v.Extra = extra
	return nil
}

func (v *bailianToolFunction) UnmarshalJSON(data []byte) error {
	type plain bailianToolFunction
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolFunctionKnown)
	if err != nil {
		return err
	}
	*v = bailianToolFunction(p)
	v.Extra = extra
	return nil
}

func (v *bailianResponse) UnmarshalJSON(data []byte) error {
	type plain bailianResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, responseKnown)
	if err != nil {
		return err
	}
	*v = bailianResponse(p)
	v.Extra = extra
	return nil
}

func (v *bailianChoice) UnmarshalJSON(data []byte) error {
	type plain bailianChoice
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, choiceKnown)
	if err != nil {
		return err
	}
	*v = bailianChoice(p)
	v.Extra = extra
	return nil
}

func (v *bailianDelta) UnmarshalJSON(data []byte) error {
	type plain bailianDelta
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, deltaKnown)
	if err != nil {
		return err
	}
	*v = bailianDelta(p)
	v.Extra = extra
	return nil
}

func (v *bailianUsage) UnmarshalJSON(data []byte) error {
	type plain bailianUsage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, usageKnown)
	if err != nil {
		return err
	}
	*v = bailianUsage(p)
	v.Extra = extra
	return nil
}

func (v *bailianPromptDetails) UnmarshalJSON(data []byte) error {
	type plain bailianPromptDetails
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, promptDetailsKnown)
	if err != nil {
		return err
	}
	*v = bailianPromptDetails(p)
	v.Extra = extra
	return nil
}

// DecodeRequest parses a Bailian-compatible request body into IR. In V1.0 the
// gateway never exposes this dialect to clients; the decoder is used by
// upstream-side tests and by the dispatch adapter's symmetric conversion.
func DecodeRequest(body []byte) (*ir.Request, error) {
	if err := requireObject(body); err != nil {
		return nil, fmt.Errorf("bailian: request: %w", err)
	}
	var wire bailianRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("bailian: decode request: %w", err)
	}
	req := &ir.Request{
		Model:       strings.TrimSpace(wire.Model),
		Stream:      wire.Stream,
		MaxTokens:   wire.MaxTokens,
		Temperature: wire.Temperature,
		TopP:        wire.TopP,
		N:           wire.N,
		ToolChoice:  cloneRaw(wire.ToolChoice),
		RawExtra:    wire.Extra,
	}
	if wire.StreamOptions != nil {
		req.StreamOptions = &ir.StreamOptions{
			IncludeUsage: wire.StreamOptions.IncludeUsage,
			Extra:        wire.StreamOptions.Extra,
		}
	}
	for _, m := range wire.Messages {
		msg := ir.Message{
			Role:       strings.ToLower(strings.TrimSpace(m.Role)),
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			Extra:      m.Extra,
		}
		content, err := decodeContent(m.Content)
		if err != nil {
			return nil, err
		}
		msg.Content = content
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, decodeToolCall(tc))
		}
		req.Messages = append(req.Messages, msg)
	}
	if len(wire.Stop) > 0 {
		var single string
		if err := json.Unmarshal(wire.Stop, &single); err == nil {
			req.Stop = []string{single}
		} else {
			var many []string
			if err := json.Unmarshal(wire.Stop, &many); err != nil {
				return nil, fmt.Errorf("bailian: stop must be a string or string array")
			}
			req.Stop = many
		}
	}
	if len(wire.ResponseFmt) > 0 {
		var shape struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(wire.ResponseFmt, &shape); err == nil {
			req.ResponseFormat = shape.Type
		}
	}
	for _, t := range wire.Tools {
		req.Tools = append(req.Tools, ir.Tool{
			Name:        strings.TrimSpace(t.Function.Name),
			Description: t.Function.Description,
			Parameters:  cloneRaw(t.Function.Parameters),
			Extra:       mergeRaw(t.Extra, t.Function.Extra),
		})
	}
	return req, nil
}

func decodeToolCall(tc bailianToolCall) ir.ToolCall {
	return ir.ToolCall{
		ID:        tc.ID,
		Name:      tc.Function.Name,
		Arguments: argumentsRaw(tc.Function.Arguments),
	}
}

func decodeContent(raw json.RawMessage) (ir.Content, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ir.Content{}, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return ir.Content{Text: text}, nil
	}
	var parts []bailianContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ir.Content{}, fmt.Errorf("bailian: content must be a string or an array")
	}
	out := ir.Content{Parts: make([]ir.ContentPart, 0, len(parts))}
	for _, part := range parts {
		p := ir.ContentPart{Type: part.Type, Text: part.Text, Extra: part.Extra}
		if part.ImageURL != nil {
			p.ImageURL = part.ImageURL.URL
			p.Extra = mergeRaw(p.Extra, part.ImageURL.Extra)
		}
		out.Parts = append(out.Parts, p)
	}
	return out, nil
}

// DecodeResponse parses a non-streaming Bailian response. A body without
// "choices" (embeddings) is preserved verbatim for the encoder.
func DecodeResponse(body []byte) (*ir.Response, error) {
	if err := requireObject(body); err != nil {
		return nil, fmt.Errorf("bailian: response: %w", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("bailian: response: %w", err)
	}
	if _, ok := probe["choices"]; !ok {
		cloned := append([]byte(nil), body...)
		return &ir.Response{Extra: map[string]json.RawMessage{"_raw_response": cloned}}, nil
	}
	var wire bailianResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("bailian: decode response: %w", err)
	}
	resp := &ir.Response{
		ID:      wire.ID,
		Object:  wire.Object,
		Model:   wire.Model,
		Created: wire.Created,
		Usage:   decodeUsage(wire.Usage),
		Extra:   wire.Extra,
	}
	for _, ch := range wire.Choices {
		choice := ir.Choice{
			Index:        ch.Index,
			FinishReason: finishReason(ch.FinishReason),
			Logprobs:     cloneRaw(ch.Logprobs),
			Extra:        ch.Extra,
		}
		if ch.Message != nil {
			m := &ir.Message{
				Role:       ch.Message.Role,
				Name:       ch.Message.Name,
				ToolCallID: ch.Message.ToolCallID,
				Extra:      ch.Message.Extra,
			}
			content, err := decodeContent(ch.Message.Content)
			if err != nil {
				return nil, err
			}
			m.Content = content
			for _, tc := range ch.Message.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, decodeToolCall(tc))
			}
			choice.Message = m
		}
		resp.Choices = append(resp.Choices, choice)
	}
	return resp, nil
}

// DecodeChunk parses one streaming data payload.
func DecodeChunk(data []byte) (*ir.Chunk, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "[DONE]" {
		return &ir.Chunk{Done: true}, nil
	}
	var wire bailianResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("bailian: malformed stream frame: %w", err)
	}
	chunk := &ir.Chunk{
		ID:      wire.ID,
		Object:  wire.Object,
		Model:   wire.Model,
		Created: wire.Created,
		Usage:   decodeUsage(wire.Usage),
		Extra:   wire.Extra,
	}
	if rawErr, ok := wire.Extra["error"]; ok {
		chunk.Error = decodeErrorFrame(rawErr)
	}
	for _, ch := range wire.Choices {
		choice := ir.Choice{
			Index:        ch.Index,
			FinishReason: finishReason(ch.FinishReason),
			Extra:        ch.Extra,
		}
		if ch.Delta != nil {
			d := &ir.Delta{Role: ch.Delta.Role, Content: ch.Delta.Content, Extra: ch.Delta.Extra}
			for _, tc := range ch.Delta.ToolCalls {
				d.ToolCalls = append(d.ToolCalls, decodeToolCall(tc))
			}
			choice.Delta = d
			if ch.Delta.Content != "" {
				chunk.HasContent = true
			}
			if chunk.Role == "" && ch.Delta.Role != "" {
				chunk.Role = ch.Delta.Role
			}
		}
		chunk.Choices = append(chunk.Choices, choice)
	}
	return chunk, nil
}

func decodeErrorFrame(raw json.RawMessage) *ir.IRError {
	var shape struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return &ir.IRError{Message: string(raw)}
	}
	code := shape.Type
	if value, ok := shape.Code.(string); ok && value != "" {
		code = value
	}
	return &ir.IRError{UpstreamCode: code, Message: shape.Message}
}

func decodeUsage(u *bailianUsage) *ir.Usage {
	if u == nil {
		return nil
	}
	in := u.PromptTokens
	if in == 0 {
		in = u.InputTokens
	}
	out := u.CompletionTokens
	if out == 0 {
		out = u.OutputTokens
	}
	usage := &ir.Usage{
		InputTokens:  in,
		OutputTokens: out,
		TotalTokens:  u.TotalTokens,
		Extra:        u.Extra,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = in + out
	}
	if u.PromptTokensDetails != nil {
		usage.SetCacheTokens(u.PromptTokensDetails.CachedTokens, 0)
		if len(u.PromptTokensDetails.Extra) > 0 {
			if usage.Extra == nil {
				usage.Extra = map[string]json.RawMessage{}
			}
			for name, value := range u.PromptTokensDetails.Extra {
				usage.Extra["prompt_tokens_details."+name] = value
			}
		}
	}
	if u.CompletionTokensDetails != nil && len(u.CompletionTokensDetails.Extra) > 0 {
		if usage.Extra == nil {
			usage.Extra = map[string]json.RawMessage{}
		}
		for name, value := range u.CompletionTokensDetails.Extra {
			usage.Extra["completion_tokens_details."+name] = value
		}
	}
	return usage
}

func finishReason(v any) string {
	switch value := v.(type) {
	case string:
		return value
	default:
		return ""
	}
}

func requireObject(body []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return fmt.Errorf("not a JSON object: %w", err)
	}
	if probe == nil {
		return fmt.Errorf("not a JSON object")
	}
	return nil
}

func knownSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

func unknownMembers(data []byte, known map[string]bool) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	for name, value := range all {
		if known[name] {
			continue
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[name] = value
	}
	return extra, nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func mergeRaw(base, over map[string]json.RawMessage) map[string]json.RawMessage {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func argumentsRaw(arguments string) json.RawMessage {
	if arguments == "" {
		return nil
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil
	}
	return encoded
}
