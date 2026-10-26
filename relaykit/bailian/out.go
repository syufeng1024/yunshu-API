package bailian

import (
	"encoding/json"
	"fmt"
	"strings"

	"yunshu.dev/relaykit/ir"
)

func (v bailianRequest) MarshalJSON() ([]byte, error) {
	type plain bailianRequest
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianStreamOptions) MarshalJSON() ([]byte, error) {
	type plain bailianStreamOptions
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianMessage) MarshalJSON() ([]byte, error) {
	type plain bailianMessage
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianContentPart) MarshalJSON() ([]byte, error) {
	type plain bailianContentPart
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianImageURL) MarshalJSON() ([]byte, error) {
	type plain bailianImageURL
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianToolCall) MarshalJSON() ([]byte, error) {
	type plain bailianToolCall
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianToolFunctionCall) MarshalJSON() ([]byte, error) {
	type plain bailianToolFunctionCall
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianTool) MarshalJSON() ([]byte, error) {
	type plain bailianTool
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianToolFunction) MarshalJSON() ([]byte, error) {
	type plain bailianToolFunction
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianResponse) MarshalJSON() ([]byte, error) {
	type plain bailianResponse
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianChoice) MarshalJSON() ([]byte, error) {
	type plain bailianChoice
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianDelta) MarshalJSON() ([]byte, error) {
	type plain bailianDelta
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianUsage) MarshalJSON() ([]byte, error) {
	type plain bailianUsage
	return marshalWithExtra(plain(v), v.Extra)
}

func (v bailianPromptDetails) MarshalJSON() ([]byte, error) {
	type plain bailianPromptDetails
	return marshalWithExtra(plain(v), v.Extra)
}

func marshalWithExtra(base any, extra map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, exists := merged[name]; !exists {
			merged[name] = value
		}
	}
	return json.Marshal(merged)
}

// EncodeRequest renders an IR request for a Bailian upstream. Vendor
// extensions (enable_search, search_options) arrive in RawExtra and are
// emitted unchanged; no OpenAI adapter is involved.
func EncodeRequest(req *ir.Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("bailian: nil request")
	}
	wire := bailianRequest{
		Model:       req.Model,
		Messages:    encodeMessages(req),
		Stream:      req.Stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		ToolChoice:  bailianToolChoice(req.ToolChoice),
		N:           req.N,
		Extra:       req.RawExtra,
	}
	if req.StreamOptions != nil {
		wire.StreamOptions = &bailianStreamOptions{
			IncludeUsage: req.StreamOptions.IncludeUsage,
			Extra:        req.StreamOptions.Extra,
		}
	}
	if len(req.Stop) > 0 {
		encoded, err := json.Marshal(req.Stop)
		if err != nil {
			return nil, fmt.Errorf("bailian: encode stop: %w", err)
		}
		wire.Stop = encoded
	}
	if req.ResponseFormat != "" {
		encoded, err := json.Marshal(map[string]string{"type": req.ResponseFormat})
		if err != nil {
			return nil, fmt.Errorf("bailian: encode response_format: %w", err)
		}
		wire.ResponseFmt = encoded
	}
	if len(req.Tools) > 0 {
		wire.Tools = make([]bailianTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			wire.Tools = append(wire.Tools, bailianTool{
				Type: "function",
				Function: bailianToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
					Extra:       t.Extra,
				},
			})
		}
	}
	return json.Marshal(wire)
}

func encodeMessages(req *ir.Request) []bailianMessage {
	msgs := make([]bailianMessage, 0, len(req.Messages)+1)
	if req.System != "" && !hasSystemFirst(req.Messages) {
		content, _ := json.Marshal(req.System)
		msgs = append(msgs, bailianMessage{Role: "system", Content: content})
	}
	for _, m := range req.Messages {
		results, rest := splitToolResults(m.Content.Parts)
		for _, p := range results {
			content, _ := json.Marshal(p.Text)
			msgs = append(msgs, bailianMessage{Role: "tool", ToolCallID: p.ToolUseID, Content: content})
		}
		if len(results) > 0 {
			if text := partsText(rest); text != "" {
				content, _ := json.Marshal(text)
				msgs = append(msgs, bailianMessage{Role: m.Role, Content: content})
			}
			continue
		}
		msg := bailianMessage{
			Role:       m.Role,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			Extra:      m.Extra,
		}
		parts := bailianParts(m.Content.Parts)
		switch {
		case len(parts) > 0:
			if len(parts) == 1 && parts[0].Type == "text" {
				content, _ := json.Marshal(parts[0].Text)
				msg.Content = content
			} else {
				content, _ := json.Marshal(parts)
				msg.Content = content
			}
		case len(m.Content.Parts) == 0:
			content, _ := json.Marshal(m.Content.Text)
			msg.Content = content
		case len(m.ToolCalls) > 0:
			msg.Content = json.RawMessage("null")
		default:
			msg.Content = json.RawMessage("[]")
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]bailianToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, bailianToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: bailianToolFunctionCall{
						Name:      tc.Name,
						Arguments: argumentsString(tc.Arguments),
					},
				})
			}
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func encodeContentPart(p ir.ContentPart) bailianContentPart {
	switch p.Type {
	case "image", "image_url":
		return bailianContentPart{
			Type:     "image_url",
			ImageURL: &bailianImageURL{URL: p.ImageURL},
			Extra:    p.Extra,
		}
	default:
		return bailianContentPart{Type: "text", Text: p.Text, Extra: p.Extra}
	}
}

func splitToolResults(parts []ir.ContentPart) ([]ir.ContentPart, []ir.ContentPart) {
	var results, rest []ir.ContentPart
	for _, p := range parts {
		if p.Type == "tool_result" {
			results = append(results, p)
			continue
		}
		rest = append(rest, p)
	}
	return results, rest
}

func bailianParts(parts []ir.ContentPart) []bailianContentPart {
	out := make([]bailianContentPart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text", "image", "image_url":
			out = append(out, encodeContentPart(p))
		}
	}
	return out
}

func partsText(parts []ir.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func hasSystemFirst(msgs []ir.Message) bool {
	for _, m := range msgs {
		if m.Role == "system" {
			return true
		}
		if m.Role != "" {
			return false
		}
	}
	return false
}

// EncodeResponse renders one non-streaming Bailian response.
func EncodeResponse(resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("bailian: nil response")
	}
	if raw, ok := resp.Extra["_raw_response"]; ok && len(raw) > 0 {
		return append([]byte(nil), raw...), nil
	}
	wire := bailianResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Created: resp.Created,
		Usage:   encodeUsage(resp.Usage),
		Extra:   stripSentinels(resp.Extra),
	}
	for _, ch := range resp.Choices {
		choice := bailianChoice{
			Index:        ch.Index,
			FinishReason: nullableString(ch.FinishReason),
			Logprobs:     ch.Logprobs,
			Extra:        ch.Extra,
		}
		if ch.Message != nil {
			msg := encodeMessage(*ch.Message)
			choice.Message = &msg
		}
		wire.Choices = append(wire.Choices, choice)
	}
	if wire.Choices == nil {
		wire.Choices = []bailianChoice{}
	}
	return json.Marshal(wire)
}

func encodeMessage(m ir.Message) bailianMessage {
	out := bailianMessage{
		Role:       m.Role,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		Extra:      m.Extra,
	}
	parts := bailianParts(m.Content.Parts)
	switch {
	case len(parts) > 0:
		if len(parts) == 1 && parts[0].Type == "text" {
			content, _ := json.Marshal(parts[0].Text)
			out.Content = content
		} else {
			content, _ := json.Marshal(parts)
			out.Content = content
		}
	case len(m.Content.Parts) == 0:
		content, _ := json.Marshal(m.Content.Text)
		out.Content = content
	case len(m.ToolCalls) > 0:
		out.Content = json.RawMessage("null")
	default:
		out.Content = json.RawMessage("[]")
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]bailianToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, bailianToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: bailianToolFunctionCall{
					Name:      tc.Name,
					Arguments: argumentsString(tc.Arguments),
				},
			})
		}
	}
	return out
}

// EncodeChunk renders one streaming payload without the data: prefix.
func EncodeChunk(chunk *ir.Chunk) ([]byte, error) {
	if chunk == nil {
		return nil, fmt.Errorf("bailian: nil chunk")
	}
	if chunk.Done {
		return append([]byte(nil), DonePayload...), nil
	}
	if chunk.Error != nil {
		return json.Marshal(map[string]any{"error": errorObject(chunk.Error)})
	}
	wire := bailianResponse{
		ID:      chunk.ID,
		Object:  "chat.completion.chunk",
		Model:   chunk.Model,
		Created: chunk.Created,
		Usage:   encodeUsage(chunk.Usage),
		Extra:   stripSentinels(chunk.Extra),
	}
	for _, ch := range chunk.Choices {
		choice := bailianChoice{
			Index:        ch.Index,
			FinishReason: nullableString(ch.FinishReason),
			Logprobs:     ch.Logprobs,
			Extra:        ch.Extra,
		}
		if ch.Delta != nil {
			delta := bailianDelta{Role: ch.Delta.Role, Content: ch.Delta.Content, Extra: ch.Delta.Extra}
			for _, tc := range ch.Delta.ToolCalls {
				index := 0
				delta.ToolCalls = append(delta.ToolCalls, bailianToolCall{
					ID:       tc.ID,
					Type:     "function",
					Index:    &index,
					Function: bailianToolFunctionCall{Name: tc.Name, Arguments: argumentsString(tc.Arguments)},
				})
			}
			choice.Delta = &delta
		}
		wire.Choices = append(wire.Choices, choice)
	}
	if wire.Choices == nil {
		wire.Choices = []bailianChoice{}
	}
	return json.Marshal(wire)
}

// EncodeSSE wraps one chunk as a complete SSE frame.
func EncodeSSE(chunk *ir.Chunk) ([]byte, error) {
	payload, err := EncodeChunk(chunk)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	return frame, nil
}

// DonePayload is the Bailian stream terminator.
var DonePayload = []byte("[DONE]")

func errorObject(e *ir.IRError) map[string]any {
	out := map[string]any{"message": e.Message, "type": "upstream_error"}
	if e.UpstreamCode != "" {
		out["code"] = e.UpstreamCode
	}
	return out
}

func encodeUsage(u *ir.Usage) *bailianUsage {
	if u == nil {
		return nil
	}
	out := &bailianUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      firstNonZero(u.TotalTokens, u.InputTokens+u.OutputTokens),
		Extra:            map[string]json.RawMessage{},
	}
	cached := u.CacheReadTokens
	if cached == 0 {
		cached = u.CacheHitTokens
	}
	if cached > 0 {
		out.PromptTokensDetails = &bailianPromptDetails{CachedTokens: cached}
	}
	for name, value := range u.Extra {
		if detail, ok := strings.CutPrefix(name, "prompt_tokens_details."); ok {
			if out.PromptTokensDetails == nil {
				out.PromptTokensDetails = &bailianPromptDetails{}
			}
			if out.PromptTokensDetails.Extra == nil {
				out.PromptTokensDetails.Extra = map[string]json.RawMessage{}
			}
			out.PromptTokensDetails.Extra[detail] = value
			continue
		}
		if detail, ok := strings.CutPrefix(name, "completion_tokens_details."); ok {
			if out.CompletionTokensDetails == nil {
				out.CompletionTokensDetails = &bailianPromptDetails{}
			}
			if out.CompletionTokensDetails.Extra == nil {
				out.CompletionTokensDetails.Extra = map[string]json.RawMessage{}
			}
			out.CompletionTokensDetails.Extra[detail] = value
			continue
		}
		out.Extra[name] = value
	}
	if len(out.Extra) == 0 {
		out.Extra = nil
	}
	return out
}

func stripSentinels(extra map[string]json.RawMessage) map[string]json.RawMessage {
	if _, ok := extra["_raw_response"]; !ok {
		return extra
	}
	out := make(map[string]json.RawMessage, len(extra)-1)
	for k, v := range extra {
		if k != "_raw_response" {
			out[k] = v
		}
	}
	return out
}

// bailianToolChoice accepts the Anthropic {"type":"tool","name":"x"} shape
// and renders the OpenAI-compatible function shape DashScope expects.
func bailianToolChoice(raw json.RawMessage) json.RawMessage {
	var shape struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &shape); err == nil && shape.Type == "tool" && shape.Name != "" {
		encoded, err := json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]any{"name": shape.Name},
		})
		if err == nil {
			return encoded
		}
	}
	return raw
}

func argumentsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func firstNonZero(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}
