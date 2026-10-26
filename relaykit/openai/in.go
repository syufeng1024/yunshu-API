package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"yunshu.dev/relaykit/ir"
)

// DecodeChatRequest parses an OpenAI-compatible chat body into IR. Unknown
// members at every nesting level are preserved in Extra.
func DecodeChatRequest(body []byte) (*ir.Request, error) {
	if err := requireObject(body); err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	var wire chatRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("openai: decode request: %w", err)
	}

	req := &ir.Request{
		Model:       strings.TrimSpace(wire.Model),
		Stream:      wire.Stream,
		MaxTokens:   wire.MaxTokens,
		Temperature: wire.Temperature,
		TopP:        wire.TopP,
		N:           wire.N,
		System:      wire.System,
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
				return nil, fmt.Errorf("openai: stop must be a string or string array")
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
			Extra:       mergeMaps(t.Extra, t.Function.Extra),
		})
	}
	return req, nil
}

func decodeToolCall(tc toolCall) ir.ToolCall {
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
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ir.Content{}, fmt.Errorf("openai: content must be a string or an array")
	}
	out := ir.Content{Parts: make([]ir.ContentPart, 0, len(parts))}
	for _, part := range parts {
		p := ir.ContentPart{
			Type:  part.Type,
			Text:  part.Text,
			Extra: part.Extra,
		}
		if part.ImageURL != nil {
			p.ImageURL = part.ImageURL.URL
			if extra := imageURLMembers(*part.ImageURL); len(extra) > 0 {
				p.Extra = mergeMaps(p.Extra, extra)
			}
		}
		out.Parts = append(out.Parts, p)
	}
	return out, nil
}

// DecodeResponse parses a non-streaming chat response. A body without a
// "choices" member (embeddings / images) is preserved verbatim under
// Extra["_raw_response"] so the matching encoder can return it unchanged.
func DecodeResponse(body []byte) (*ir.Response, error) {
	if err := requireObject(body); err != nil {
		return nil, fmt.Errorf("openai: response: %w", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("openai: response: %w", err)
	}
	if _, ok := probe["choices"]; !ok {
		cloned := append([]byte(nil), body...)
		return &ir.Response{Extra: map[string]json.RawMessage{"_raw_response": cloned}}, nil
	}
	var wire chatResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
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

// DecodeChunk parses one data payload of an OpenAI-compatible stream. A
// payload carrying an "error" object yields Chunk.Error with no choices; it
// never aborts with a Go error, because the relay still has to close the
// client stream deliberately.
func DecodeChunk(data []byte) (*ir.Chunk, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "[DONE]" {
		return &ir.Chunk{Done: true}, nil
	}
	var wire chatResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("openai: malformed stream frame: %w", err)
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
		// The raw member stays in Extra on purpose: existing relay code detects
		// an upstream error frame by its presence, and Extra is the lossless
		// representation.
	}
	for _, ch := range wire.Choices {
		choice := ir.Choice{
			Index:        ch.Index,
			FinishReason: finishReason(ch.FinishReason),
			Extra:        ch.Extra,
		}
		if ch.Delta != nil {
			d := &ir.Delta{
				Role:    ch.Delta.Role,
				Content: ch.Delta.Content,
				Extra:   ch.Delta.Extra,
			}
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
	switch v := shape.Code.(type) {
	case string:
		if v != "" {
			code = v
		}
	}
	return &ir.IRError{UpstreamCode: code, Message: shape.Message}
}

func decodeUsage(u *usage) *ir.Usage {
	if u == nil {
		return nil
	}
	out := &ir.Usage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
		Extra:        u.Extra,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	if u.PromptTokensDetails != nil {
		out.SetCacheTokens(u.PromptTokensDetails.CachedTokens, 0)
		out.Extra = mergeNested(out.Extra, "prompt_tokens_details", u.PromptTokensDetails.Extra)
	}
	if u.CompletionTokensDetails != nil {
		out.Extra = mergeNested(out.Extra, "completion_tokens_details", u.CompletionTokensDetails.Extra)
	}
	return out
}

func finishReason(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case nil:
		return ""
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

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func mergeNested(base map[string]json.RawMessage, prefix string, nested map[string]json.RawMessage) map[string]json.RawMessage {
	if len(nested) == 0 {
		return base
	}
	if base == nil {
		base = map[string]json.RawMessage{}
	}
	for name, value := range nested {
		base[prefix+"."+name] = value
	}
	return base
}

func mergeMaps(base, over map[string]json.RawMessage) map[string]json.RawMessage {
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

// argumentsRaw keeps a JSON string argument list as a JSON string value. The
// OpenAI wire type is a string; the IR stores whatever JSON value arrived so an
// Anthropic object-shaped input can round-trip without lossy stringification.
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
