package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"yunshu.dev/relaykit/ir"
)

// DecodeRequest parses an Anthropic-compatible messages body into IR. Unknown
// top-level members such as metadata, top_k and thinking survive in RawExtra.
// max_tokens is mandatory on this protocol and is rejected when absent.
func DecodeRequest(body []byte) (*ir.Request, error) {
	if err := requireObject(body); err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	var wire messagesRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("anthropic: decode request: %w", err)
	}
	req := &ir.Request{
		Model:       strings.TrimSpace(wire.Model),
		MaxTokens:   wire.MaxTokens,
		Temperature: wire.Temperature,
		TopP:        wire.TopP,
		Stop:        wire.StopSequences,
		Stream:      wire.Stream,
		ToolChoice:  cloneRaw(wire.ToolChoice),
		RawExtra:    wire.Extra,
	}
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	req.System = decodeSystem(wire.System)

	for _, m := range wire.Messages {
		msg := ir.Message{Role: strings.ToLower(strings.TrimSpace(m.Role)), Extra: m.Extra}
		content, calls, err := decodeContent(m.Content)
		if err != nil {
			return nil, err
		}
		msg.Content = content
		msg.ToolCalls = calls
		req.Messages = append(req.Messages, msg)
	}
	for _, t := range wire.Tools {
		req.Tools = append(req.Tools, ir.Tool{
			Name:        strings.TrimSpace(t.Name),
			Description: t.Description,
			Parameters:  cloneRaw(t.InputSchema),
			Extra:       t.Extra,
		})
	}
	return req, nil
}

func decodeSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			b.WriteString(block.Text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func decodeContent(raw json.RawMessage) (ir.Content, []ir.ToolCall, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ir.Content{}, nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return ir.Content{Text: text}, nil, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ir.Content{}, nil, fmt.Errorf("anthropic: content must be a string or block array")
	}
	out := ir.Content{Parts: make([]ir.ContentPart, 0, len(blocks))}
	var calls []ir.ToolCall
	for _, block := range blocks {
		part := ir.ContentPart{Type: block.Type, Text: block.Text, Extra: block.Extra}
		switch block.Type {
		case "text":
			// Text is already set.
		case "image":
			if block.Source == nil {
				return ir.Content{}, nil, fmt.Errorf("anthropic: image block without source")
			}
			part.MediaType = block.Source.MediaType
			part.Extra = mergeRaw(part.Extra, block.Source.Extra)
			if block.Source.URL != "" {
				part.ImageURL = block.Source.URL
			} else if block.Source.Data != "" {
				part.ImageURL = "data:" + block.Source.MediaType + ";base64," + block.Source.Data
			}
		case "tool_use":
			part.ID = block.ID
			part.Name = block.Name
			part.Input = cloneRaw(block.Input)
			calls = append(calls, ir.ToolCall{ID: block.ID, Name: block.Name, Arguments: cloneRaw(block.Input)})
		case "tool_result":
			part.ToolUseID = block.ToolUseID
			part.Text = toolResultText(block.Content)
			if block.IsError != nil && *block.IsError {
				part.Extra = setExtra(part.Extra, "is_error", json.RawMessage("true"))
			}
		}
		out.Parts = append(out.Parts, part)
	}
	return out, calls, nil
}

func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "text" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// DecodeResponse parses a non-streaming messages response.
func DecodeResponse(body []byte) (*ir.Response, error) {
	if err := requireObject(body); err != nil {
		return nil, fmt.Errorf("anthropic: response: %w", err)
	}
	var wire messagesResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	content, calls, err := decodeBlocks(wire.Content)
	if err != nil {
		return nil, err
	}
	msg := &ir.Message{
		Role:      firstNonEmpty(wire.Role, "assistant"),
		Content:   content,
		ToolCalls: calls,
	}
	resp := &ir.Response{
		ID:           wire.ID,
		Object:       firstNonEmpty(wire.Type, "message"),
		Model:        wire.Model,
		StopSequence: wire.StopSequence,
		Usage:        decodeUsage(wire.Usage),
		Extra:        wire.Extra,
		Choices: []ir.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: mapStopReason(wire.StopReason),
		}},
	}
	return resp, nil
}

func decodeBlocks(blocks []contentBlock) (ir.Content, []ir.ToolCall, error) {
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return ir.Content{}, nil, fmt.Errorf("anthropic: re-encode content blocks: %w", err)
	}
	return decodeContent(encoded)
}

// DecodeEvent parses one Anthropic SSE event. Upstream errors become
// Chunk.Error instead of a Go error so the relay can emit a well-formed
// client error frame; malformed JSON is still returned as an error.
func DecodeEvent(event string, data []byte) (*ir.Chunk, error) {
	var wire streamEvent
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("anthropic: malformed event %q: %w", event, err)
	}
	kind := firstNonEmpty(wire.Type, event)
	chunk := &ir.Chunk{Extra: wire.Extra}
	switch kind {
	case "message_start":
		if wire.Message != nil {
			chunk.ID = wire.Message.ID
			chunk.Model = wire.Message.Model
			chunk.Role = firstNonEmpty(wire.Message.Role, "assistant")
			chunk.Usage = decodeUsage(wire.Message.Usage)
			chunk.Choices = []ir.Choice{{Index: 0, Delta: &ir.Delta{Role: chunk.Role}}}
		}
		return chunk, nil
	case "content_block_start":
		if wire.Content != nil && wire.Content.Type == "tool_use" {
			index := intValue(wire.Index)
			chunk.Choices = []ir.Choice{{Index: index, Delta: &ir.Delta{ToolCalls: []ir.ToolCall{{
				ID: wire.Content.ID, Name: wire.Content.Name,
			}}}}}
		}
		return chunk, nil
	case "content_block_delta":
		if wire.Delta != nil {
			delta := &ir.Delta{Extra: wire.Delta.Extra}
			switch wire.Delta.Type {
			case "text_delta":
				delta.Content = wire.Delta.Text
				if wire.Delta.Text != "" {
					chunk.HasContent = true
				}
			case "input_json_delta":
				delta.ToolCalls = []ir.ToolCall{{Arguments: marshalString(wire.Delta.PartialJSON)}}
			}
			chunk.Choices = []ir.Choice{{Index: intValue(wire.Index), Delta: delta}}
		}
		return chunk, nil
	case "message_delta":
		chunk.Usage = decodeUsage(wire.Usage)
		if wire.Delta != nil && wire.Delta.StopReason != "" {
			chunk.Choices = []ir.Choice{{Index: 0, FinishReason: mapStopReason(wire.Delta.StopReason)}}
		}
		return chunk, nil
	case "message_stop":
		chunk.Done = true
		return chunk, nil
	case "error":
		chunk.Error = &ir.IRError{Message: "upstream error"}
		if wire.Error != nil {
			chunk.Error.UpstreamCode = wire.Error.Type
			chunk.Error.Message = wire.Error.Message
		}
		return chunk, nil
	default:
		// content_block_stop / ping have no IR content.
		return chunk, nil
	}
}

func decodeUsage(u *usage) *ir.Usage {
	if u == nil {
		return nil
	}
	out := &ir.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.InputTokens + u.OutputTokens,
		Extra:        u.Extra,
	}
	out.SetCacheTokens(u.CacheReadInputTokens, u.CacheCreationInputTokens)
	return out
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
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

func marshalString(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return encoded
}

func mustMarshalString(s string) json.RawMessage { return marshalString(s) }

func setExtra(extra map[string]json.RawMessage, name string, value json.RawMessage) map[string]json.RawMessage {
	if extra == nil {
		extra = map[string]json.RawMessage{}
	}
	extra[name] = value
	return extra
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

func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
