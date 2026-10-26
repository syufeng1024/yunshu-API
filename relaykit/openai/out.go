package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"yunshu.dev/relaykit/ir"
)

// EncodeChatRequest renders an IR request as an OpenAI-compatible chat body.
// Unknown members are re-emitted from IR RawExtra; a modeled member always
// wins over a same-named Extra member because the IR field is what the
// pipeline acted on.
func EncodeChatRequest(req *ir.Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("openai: nil request")
	}
	wire := chatRequest{
		Model:       req.Model,
		Messages:    encodeMessages(req),
		Stream:      req.Stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		ToolChoice:  openAIToolChoice(req.ToolChoice),
		N:           req.N,
		Extra:       req.RawExtra,
	}
	if req.StreamOptions != nil {
		wire.StreamOptions = &streamOptions{
			IncludeUsage: req.StreamOptions.IncludeUsage,
			Extra:        req.StreamOptions.Extra,
		}
	}
	if len(req.Stop) > 0 {
		encoded, err := json.Marshal(req.Stop)
		if err != nil {
			return nil, fmt.Errorf("openai: encode stop: %w", err)
		}
		wire.Stop = encoded
	}
	if req.ResponseFormat != "" {
		encoded, err := json.Marshal(map[string]string{"type": req.ResponseFormat})
		if err != nil {
			return nil, fmt.Errorf("openai: encode response_format: %w", err)
		}
		wire.ResponseFmt = encoded
	}
	if len(req.Tools) > 0 {
		wire.Tools = make([]tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			wire.Tools = append(wire.Tools, tool{
				Type: "function",
				Function: toolFunction{
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

func encodeMessages(req *ir.Request) []message {
	msgs := make([]message, 0, len(req.Messages)+1)
	if req.System != "" && !hasSystemFirst(req.Messages) {
		content, _ := json.Marshal(req.System)
		msgs = append(msgs, message{Role: "system", Content: content})
	}
	for _, m := range req.Messages {
		// An Anthropic tool_result block has no OpenAI message equivalent; it
		// becomes one role:"tool" message per result.
		results, rest := splitToolResults(m.Content.Parts)
		for _, p := range results {
			content, _ := json.Marshal(p.Text)
			msgs = append(msgs, message{Role: "tool", ToolCallID: p.ToolUseID, Content: content})
		}
		if len(results) > 0 {
			if text := partsText(rest); text != "" {
				content, _ := json.Marshal(text)
				msgs = append(msgs, message{Role: m.Role, Content: content})
			}
			continue
		}
		msg := message{
			Role:       m.Role,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			Extra:      m.Extra,
		}
		// Parts the OpenAI dialect cannot represent (tool_use / tool_result)
		// are carried by ToolCalls or expanded above.
		parts := openAIParts(m.Content.Parts)
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
			msg.ToolCalls = make([]toolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, toolCall{
					ID:   tc.ID,
					Type: "function",
					Function: toolFunctionCall{
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

func openAIParts(parts []ir.ContentPart) []contentPart {
	out := make([]contentPart, 0, len(parts))
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

func encodeContentPart(p ir.ContentPart) contentPart {
	switch p.Type {
	case "image", "image_url":
		part := contentPart{Type: "image_url", ImageURL: &imageURL{URL: p.ImageURL}}
		extra := map[string]json.RawMessage{}
		for name, value := range p.Extra {
			switch name {
			case "detail":
				var detail string
				if err := json.Unmarshal(value, &detail); err == nil {
					part.ImageURL.Detail = detail
					continue
				}
				extra[name] = value
			default:
				extra[name] = value
			}
		}
		if len(extra) > 0 {
			part.Extra = extra
		}
		return part
	default:
		return contentPart{Type: "text", Text: p.Text, Extra: p.Extra}
	}
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

// EncodeResponse renders one non-streaming response. A raw-preserved body is
// returned unchanged (embeddings / images have no choice list).
func EncodeResponse(resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("openai: nil response")
	}
	if raw, ok := resp.Extra["_raw_response"]; ok && len(raw) > 0 {
		return append([]byte(nil), raw...), nil
	}
	wire := chatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.Created,
		Model:   resp.Model,
		Usage:   encodeUsage(resp.Usage),
		Extra:   stripSentinels(resp.Extra),
	}
	for _, ch := range resp.Choices {
		choice := choice{
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
		wire.Choices = []choice{}
	}
	return json.Marshal(wire)
}

func encodeMessage(m ir.Message) message {
	out := message{
		Role:       m.Role,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		Extra:      m.Extra,
	}
	parts := openAIParts(m.Content.Parts)
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
		out.ToolCalls = make([]toolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, toolCall{
				ID:   tc.ID,
				Type: "function",
				Function: toolFunctionCall{
					Name:      tc.Name,
					Arguments: argumentsString(tc.Arguments),
				},
			})
		}
	}
	return out
}

// EncodeChunk renders one streaming frame payload (without the "data: "
// prefix or the blank-line terminator). A chunk carrying Error becomes the
// PRD E.4 OpenAI error payload: no event line, one data line, then close.
func EncodeChunk(chunk *ir.Chunk) ([]byte, error) {
	if chunk == nil {
		return nil, fmt.Errorf("openai: nil chunk")
	}
	if chunk.Done {
		return append([]byte(nil), DonePayload...), nil
	}
	if chunk.Error != nil {
		return json.Marshal(map[string]any{"error": errorObject(chunk.Error)})
	}
	wire := chatResponse{
		ID:      chunk.ID,
		Object:  "chat.completion.chunk",
		Created: chunk.Created,
		Model:   chunk.Model,
		Usage:   encodeUsage(chunk.Usage),
		Extra:   stripSentinels(chunk.Extra),
	}
	for _, ch := range chunk.Choices {
		choice := choice{
			Index:        ch.Index,
			FinishReason: nullableString(ch.FinishReason),
			Logprobs:     ch.Logprobs,
			Extra:        ch.Extra,
		}
		if ch.Delta != nil {
			delta := delta{Role: ch.Delta.Role, Content: ch.Delta.Content, Extra: ch.Delta.Extra}
			for _, tc := range ch.Delta.ToolCalls {
				index := 0
				delta.ToolCalls = append(delta.ToolCalls, toolCall{
					ID:       tc.ID,
					Type:     "function",
					Index:    &index,
					Function: toolFunctionCall{Name: tc.Name, Arguments: argumentsString(tc.Arguments)},
				})
			}
			choice.Delta = &delta
		}
		wire.Choices = append(wire.Choices, choice)
	}
	if wire.Choices == nil {
		wire.Choices = []choice{}
	}
	return json.Marshal(wire)
}

// EncodeSSE wraps one chunk as a complete OpenAI SSE frame, terminator
// included. Callers that need to inspect the payload use EncodeChunk.
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

// DonePayload is the OpenAI stream terminator.
var DonePayload = []byte("[DONE]")

func errorObject(e *ir.IRError) map[string]any {
	out := map[string]any{
		"message": e.Message,
		"type":    "upstream_error",
	}
	if e.UpstreamCode != "" {
		out["code"] = e.UpstreamCode
	}
	return out
}

func encodeUsage(u *ir.Usage) *usage {
	if u == nil {
		return nil
	}
	out := &usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      firstNonZero(u.TotalTokens, u.InputTokens+u.OutputTokens),
		Extra:            make(map[string]json.RawMessage),
	}
	cached := u.CacheReadTokens
	if cached == 0 {
		cached = u.CacheHitTokens
	}
	if cached > 0 {
		out.PromptTokensDetails = &promptTokensDetails{CachedTokens: cached}
	}
	for name, value := range u.Extra {
		if detail := splitNested(name, "prompt_tokens_details"); detail != "" {
			if out.PromptTokensDetails == nil {
				out.PromptTokensDetails = &promptTokensDetails{}
			}
			out.PromptTokensDetails.Extra = setRaw(out.PromptTokensDetails.Extra, detail, value)
			continue
		}
		if detail := splitNested(name, "completion_tokens_details"); detail != "" {
			if out.CompletionTokensDetails == nil {
				out.CompletionTokensDetails = &completionTokensDetails{}
			}
			out.CompletionTokensDetails.Extra = setRaw(out.CompletionTokensDetails.Extra, detail, value)
			continue
		}
		out.Extra = setRaw(out.Extra, name, value)
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

// EncodeEmbeddingsRequest renders the embeddings endpoint body. The upstream
// remains OpenAI-compatible; no conversion shortcut is taken.
func EncodeEmbeddingsRequest(req *ir.Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("openai: nil request")
	}
	wire := embeddingsRequest{Model: req.Model, Extra: req.RawExtra}
	input, err := json.Marshal(req.EmbedInputs)
	if err != nil {
		return nil, fmt.Errorf("openai: encode input: %w", err)
	}
	if len(req.EmbedInputs) == 1 {
		input, err = json.Marshal(req.EmbedInputs[0])
		if err != nil {
			return nil, fmt.Errorf("openai: encode input: %w", err)
		}
	}
	wire.Input = input
	return json.Marshal(wire)
}

// EncodeImageRequest renders the image generation endpoint body.
func EncodeImageRequest(req *ir.Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("openai: nil request")
	}
	return json.Marshal(imageRequest{
		Model:          req.Model,
		Prompt:         req.Prompt,
		N:              req.N,
		Size:           req.Size,
		ResponseFormat: req.ResponseFormat,
		Extra:          req.RawExtra,
	})
}

func openAIToolChoice(raw json.RawMessage) json.RawMessage {
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
