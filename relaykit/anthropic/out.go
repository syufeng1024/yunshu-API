package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"yunshu.dev/relaykit/ir"
)

// EncodeRequest renders an IR request as an Anthropic-compatible messages
// body. max_tokens is mandatory: a missing value is rejected locally instead
// of sending a body the upstream will refuse.
func EncodeRequest(req *ir.Request) ([]byte, error) {
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	wire := messagesRequest{
		Model:         req.Model,
		MaxTokens:     req.MaxTokens,
		Messages:      encodeMessages(req),
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		Stream:        req.Stream,
		ToolChoice:    anthropicToolChoice(req.ToolChoice),
		Extra:         req.RawExtra,
	}
	system := collectSystem(req)
	if system != "" {
		encoded, err := json.Marshal(system)
		if err != nil {
			return nil, fmt.Errorf("anthropic: encode system: %w", err)
		}
		wire.System = encoded
	}
	if len(req.Tools) > 0 {
		wire.Tools = make([]tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			wire.Tools = append(wire.Tools, tool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Parameters,
				Extra:       t.Extra,
			})
		}
	}
	return json.Marshal(wire)
}

func collectSystem(req *ir.Request) string {
	system := req.System
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		text := m.Content.Text
		if len(m.Content.Parts) > 0 {
			var b strings.Builder
			for _, p := range m.Content.Parts {
				if p.Type == "text" {
					b.WriteString(p.Text)
				}
			}
			text = b.String()
		}
		if text == "" {
			continue
		}
		if system != "" {
			system += "\n"
		}
		system += text
	}
	return system
}

func encodeMessages(req *ir.Request) []message {
	messages := make([]message, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		out := message{Role: m.Role, Extra: m.Extra}
		if out.Role == "tool" {
			out.Role = "user"
			out.Content = encodeToolResult(m)
		} else {
			blocks := encodeBlocks(m)
			out.Content = encodeContentBlocks(blocks)
		}
		messages = append(messages, out)
	}
	return messages
}

func encodeToolResult(m ir.Message) json.RawMessage {
	text := m.Content.Text
	if len(m.Content.Parts) > 0 {
		var b strings.Builder
		for _, p := range m.Content.Parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		text = b.String()
	}
	block := map[string]any{
		"type":        "tool_result",
		"tool_use_id": m.ToolCallID,
		"content":     text,
	}
	encoded, _ := json.Marshal([]any{block})
	return encoded
}

func encodeContentBlocks(blocks []contentBlock) json.RawMessage {
	if len(blocks) == 1 {
		if text, ok := plainTextBlock(blocks[0]); ok {
			encoded, _ := json.Marshal(text)
			return encoded
		}
	}
	encoded, _ := json.Marshal(blocks)
	return encoded
}

func plainTextBlock(block contentBlock) (string, bool) {
	if block.Type == "text" && len(block.Extra) == 0 {
		return block.Text, true
	}
	return "", false
}

func encodeBlocks(m ir.Message) []contentBlock {
	blocks := make([]contentBlock, 0, len(m.Content.Parts)+len(m.ToolCalls))
	// tool_use blocks are emitted once, from ToolCalls. The matching IR part is
	// kept only for its Extra (cache_control and friends) so the block is not
	// duplicated.
	toolParts := map[string]ir.ContentPart{}
	if len(m.Content.Parts) == 0 {
		if m.Content.Text != "" || len(m.ToolCalls) == 0 {
			blocks = append(blocks, contentBlock{Type: "text", Text: m.Content.Text})
		}
	} else {
		for _, p := range m.Content.Parts {
			if p.Type == "tool_use" {
				toolParts[p.ID] = p
				continue
			}
			blocks = append(blocks, encodeBlock(p))
		}
	}
	for _, tc := range m.ToolCalls {
		var input json.RawMessage
		if len(tc.Arguments) > 0 {
			var s string
			if err := json.Unmarshal(tc.Arguments, &s); err == nil {
				input = json.RawMessage(s)
			} else {
				input = tc.Arguments
			}
		}
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		extra := map[string]json.RawMessage(nil)
		if part, ok := toolParts[tc.ID]; ok {
			extra = part.Extra
		}
		blocks = append(blocks, contentBlock{
			Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input, Extra: extra,
		})
	}
	return blocks
}

func encodeBlock(p ir.ContentPart) contentBlock {
	switch p.Type {
	case "image", "image_url":
		source := &imageSource{}
		mediaType, data, isData := parseDataURL(p.ImageURL)
		if isData {
			source.Type = "base64"
			source.MediaType = firstNonEmpty(p.MediaType, mediaType)
			source.Data = data
		} else {
			source.Type = "url"
			source.URL = p.ImageURL
		}
		return contentBlock{Type: "image", Source: source, Extra: p.Extra}
	case "tool_use":
		input := p.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return contentBlock{Type: "tool_use", ID: p.ID, Name: p.Name, Input: input, Extra: p.Extra}
	case "tool_result":
		return contentBlock{
			Type: "tool_result", ToolUseID: p.ToolUseID, Content: marshalText(p.Text), Extra: p.Extra,
		}
	default:
		return contentBlock{Type: "text", Text: p.Text, Extra: p.Extra}
	}
}

func parseDataURL(raw string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(raw, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(raw, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := rest[:comma]
	data = rest[comma+1:]
	meta = strings.TrimSuffix(meta, ";base64")
	if meta == "" {
		meta = "application/octet-stream"
	}
	return meta, data, true
}

// EncodeResponse renders one non-streaming messages response.
func EncodeResponse(resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("anthropic: nil response")
	}
	if raw, ok := resp.Extra["_raw_response"]; ok && len(raw) > 0 {
		return append([]byte(nil), raw...), nil
	}
	wire := messagesResponse{
		ID:           resp.ID,
		Type:         "message",
		Role:         "assistant",
		Model:        resp.Model,
		Content:      []contentBlock{},
		StopReason:   "end_turn",
		StopSequence: resp.StopSequence,
		Extra:        stripSentinels(resp.Extra),
	}
	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		if ch.Message != nil {
			wire.Content = encodeBlocks(*ch.Message)
		}
		wire.StopReason = reverseStopReason(ch.FinishReason)
	}
	wire.Usage = encodeUsage(resp.Usage)
	return json.Marshal(wire)
}

func reverseStopReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return firstNonEmpty(reason, "end_turn")
	}
}

func anthropicToolChoice(raw json.RawMessage) json.RawMessage {
	var shape struct {
		Type     string `json:"type"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &shape); err == nil && shape.Type == "function" &&
		shape.Function != nil {
		encoded, err := json.Marshal(map[string]any{"type": "tool", "name": shape.Function.Name})
		if err == nil {
			return encoded
		}
	}
	return raw
}

func encodeUsage(u *ir.Usage) *usage {
	if u == nil {
		return nil
	}
	out := &usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadTokens,
		CacheCreationInputTokens: u.CacheWriteTokens,
		Extra:                    u.Extra,
	}
	if out.CacheReadInputTokens == 0 {
		out.CacheReadInputTokens = u.CacheHitTokens
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

// Event is one outbound Anthropic SSE event.
type Event struct {
	Name string
	Data []byte
}

// Bytes renders the wire form of one event, including the blank-line
// terminator.
func (e Event) Bytes() []byte {
	frame := make([]byte, 0, len(e.Name)+len(e.Data)+16)
	if e.Name != "" {
		frame = append(frame, "event: "...)
		frame = append(frame, e.Name...)
		frame = append(frame, '\n')
	}
	frame = append(frame, "data: "...)
	frame = append(frame, e.Data...)
	frame = append(frame, '\n', '\n')
	return frame
}

// EncodeChunkSSE is the stateless chunk -> frame-text mapping. It is what the
// frame tests exercise; the relay uses StreamEncoder when it also needs the
// message_start / content_block_stop sequencing.
func EncodeChunkSSE(chunk *ir.Chunk) ([]byte, error) {
	events, err := chunkEvents(chunk)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, ev := range events {
		out = append(out, ev.Bytes()...)
	}
	return out, nil
}

func chunkEvents(chunk *ir.Chunk) ([]Event, error) {
	if chunk == nil {
		return nil, fmt.Errorf("anthropic: nil chunk")
	}
	if chunk.Error != nil {
		payload := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    firstNonEmpty(chunk.Error.UpstreamCode, "upstream_error"),
				"message": chunk.Error.Message,
			},
		}
		return []Event{{Name: "error", Data: marshalJSON(payload)}}, nil
	}
	if chunk.Done {
		return []Event{{Name: "message_stop", Data: marshalJSON(map[string]any{"type": "message_stop"})}}, nil
	}
	var events []Event
	if chunk.Role != "" {
		message := map[string]any{
			"id": chunk.ID, "type": "message", "role": chunk.Role, "model": chunk.Model,
			"content": []any{}, "stop_reason": nil,
		}
		if chunk.Usage != nil {
			message["usage"] = map[string]any{
				"input_tokens": chunk.Usage.InputTokens, "output_tokens": chunk.Usage.OutputTokens,
			}
		}
		events = append(events, Event{Name: "message_start", Data: marshalJSON(map[string]any{
			"type": "message_start", "message": message,
		})})
	}
	for _, ch := range chunk.Choices {
		if ch.Delta != nil {
			for _, tc := range ch.Delta.ToolCalls {
				if tc.Name != "" || tc.ID != "" {
					events = append(events, Event{Name: "content_block_start", Data: marshalJSON(map[string]any{
						"type": "content_block_start", "index": ch.Index,
						"content_block": map[string]any{
							"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": map[string]any{},
						},
					})})
				}
				if len(tc.Arguments) > 0 {
					events = append(events, Event{Name: "content_block_delta", Data: marshalJSON(map[string]any{
						"type": "content_block_delta", "index": ch.Index,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": argumentsString(tc.Arguments)},
					})})
				}
			}
			if ch.Delta.Content != "" {
				events = append(events, Event{Name: "content_block_delta", Data: marshalJSON(map[string]any{
					"type": "content_block_delta", "index": ch.Index,
					"delta": map[string]any{"type": "text_delta", "text": ch.Delta.Content},
				})})
			}
		}
		if ch.FinishReason != "" {
			payload := map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": reverseStopReason(ch.FinishReason)},
			}
			if chunk.Usage != nil {
				payload["usage"] = map[string]any{"output_tokens": chunk.Usage.OutputTokens}
			}
			events = append(events, Event{Name: "message_delta", Data: marshalJSON(payload)})
		}
	}
	if chunk.Usage != nil && len(chunk.Choices) == 0 {
		events = append(events, Event{Name: "message_delta", Data: marshalJSON(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": nil},
			"usage": map[string]any{"output_tokens": chunk.Usage.OutputTokens},
		})})
	}
	if len(events) == 0 {
		events = append(events, Event{Name: "ping", Data: marshalJSON(map[string]any{"type": "ping"})})
	}
	return events, nil
}

// StreamEncoder turns IR chunks into the Anthropic event sequence. It holds no
// I/O state; the caller writes and flushes each event.
type StreamEncoder struct {
	started bool
	block   bool
}

// Started reports whether message_start has been emitted.
func (e *StreamEncoder) Started() bool { return e.started }

// Start emits message_start and opens the first content block.
func (e *StreamEncoder) Start(id, model string, inputTokens int64) []Event {
	if e.started {
		return nil
	}
	e.started = true
	e.block = true
	start := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
		},
	}
	block := map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}
	return []Event{{Name: "message_start", Data: marshalJSON(start)},
		{Name: "content_block_start", Data: marshalJSON(block)}}
}

// Content emits one text delta.
func (e *StreamEncoder) Content(text string) []Event {
	if text == "" {
		return nil
	}
	if !e.started {
		e.started = true
		e.block = true
	}
	payload := map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}
	return []Event{{Name: "content_block_delta", Data: marshalJSON(payload)}}
}

// Finish closes the content block and the message.
func (e *StreamEncoder) Finish(stopReason string, outputTokens int64) []Event {
	if !e.started {
		return nil
	}
	var out []Event
	if e.block {
		out = append(out, Event{Name: "content_block_stop", Data: marshalJSON(map[string]any{
			"type": "content_block_stop", "index": 0,
		})})
		e.block = false
	}
	out = append(out, Event{Name: "message_delta", Data: marshalJSON(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": reverseStopReason(stopReason)},
		"usage": map[string]any{"output_tokens": outputTokens},
	})})
	out = append(out, Event{Name: "message_stop", Data: marshalJSON(map[string]any{
		"type": "message_stop",
	})})
	return out
}

func marshalJSON(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return encoded
}

func marshalText(s string) json.RawMessage {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return encoded
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
