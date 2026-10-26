package anthropic

import "encoding/json"

// Anthropic-compatible wire shapes. The conversion layer is deliberately
// local to relaykit; no internal/ type is imported.
//
// metadata is intentionally not modeled: it travels in Extra so
// metadata.user_id (and any future member) survives a conversion it does not
// participate in. Every struct below keeps unknown members by the same rule.

type messagesRequest struct {
	Model         string                     `json:"model"`
	MaxTokens     *int64                     `json:"max_tokens,omitempty"`
	System        json.RawMessage            `json:"system,omitempty"`
	Messages      []message                  `json:"messages"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	StopSequences []string                   `json:"stop_sequences,omitempty"`
	Stream        bool                       `json:"stream,omitempty"`
	Tools         []tool                     `json:"tools,omitempty"`
	ToolChoice    json.RawMessage            `json:"tool_choice,omitempty"`
	Extra         map[string]json.RawMessage `json:"-"`
}

type message struct {
	Role    string                     `json:"role"`
	Content json.RawMessage            `json:"content"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type contentBlock struct {
	Type      string                     `json:"type"`
	Text      string                     `json:"text,omitempty"`
	Source    *imageSource               `json:"source,omitempty"`
	ID        string                     `json:"id,omitempty"`
	Name      string                     `json:"name,omitempty"`
	Input     json.RawMessage            `json:"input,omitempty"`
	ToolUseID string                     `json:"tool_use_id,omitempty"`
	Content   json.RawMessage            `json:"content,omitempty"`
	IsError   *bool                      `json:"is_error,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type imageSource struct {
	Type      string                     `json:"type"`
	MediaType string                     `json:"media_type,omitempty"`
	Data      string                     `json:"data,omitempty"`
	URL       string                     `json:"url,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type tool struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	InputSchema json.RawMessage            `json:"input_schema,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

type messagesResponse struct {
	ID           string                     `json:"id"`
	Type         string                     `json:"type"`
	Role         string                     `json:"role"`
	Model        string                     `json:"model"`
	Content      []contentBlock             `json:"content"`
	StopReason   string                     `json:"stop_reason"`
	StopSequence string                     `json:"stop_sequence"`
	Usage        *usage                     `json:"usage,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type usage struct {
	InputTokens              int64                      `json:"input_tokens"`
	OutputTokens             int64                      `json:"output_tokens"`
	CacheReadInputTokens     int64                      `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64                      `json:"cache_creation_input_tokens,omitempty"`
	Extra                    map[string]json.RawMessage `json:"-"`
}

type streamEvent struct {
	Type    string                     `json:"type"`
	Index   *int                       `json:"index,omitempty"`
	Message *streamMessage             `json:"message,omitempty"`
	Content *streamContentBlock        `json:"content_block,omitempty"`
	Delta   *streamDelta               `json:"delta,omitempty"`
	Usage   *usage                     `json:"usage,omitempty"`
	Error   *streamError               `json:"error,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type streamMessage struct {
	ID      string                     `json:"id"`
	Type    string                     `json:"type"`
	Role    string                     `json:"role"`
	Model   string                     `json:"model"`
	Content []contentBlock             `json:"content"`
	Usage   *usage                     `json:"usage,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type streamContentBlock struct {
	Type  string                     `json:"type"`
	ID    string                     `json:"id,omitempty"`
	Name  string                     `json:"name,omitempty"`
	Text  string                     `json:"text,omitempty"`
	Input json.RawMessage            `json:"input,omitempty"`
	Extra map[string]json.RawMessage `json:"-"`
}

type streamDelta struct {
	Type        string                     `json:"type"`
	Text        string                     `json:"text,omitempty"`
	PartialJSON string                     `json:"partial_json,omitempty"`
	StopReason  string                     `json:"stop_reason,omitempty"`
	StopSeq     string                     `json:"stop_sequence,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

var requestKnown = knownSet("model", "max_tokens", "system", "messages",
	"temperature", "top_p", "stop_sequences", "stream", "tools", "tool_choice")
var messageKnown = knownSet("role", "content")
var blockKnown = knownSet("type", "text", "source", "id", "name", "input",
	"tool_use_id", "content", "is_error")
var sourceKnown = knownSet("type", "media_type", "data", "url")
var toolKnown = knownSet("name", "description", "input_schema")
var responseKnown = knownSet("id", "type", "role", "model", "content",
	"stop_reason", "stop_sequence", "usage")
var usageKnown = knownSet("input_tokens", "output_tokens",
	"cache_read_input_tokens", "cache_creation_input_tokens")
var eventKnown = knownSet("type", "index", "message", "content_block", "delta", "usage", "error")
var streamMessageKnown = knownSet("id", "type", "role", "model", "content", "usage")
var streamBlockKnown = knownSet("type", "id", "name", "text", "input")
var streamDeltaKnown = knownSet("type", "text", "partial_json", "stop_reason", "stop_sequence")

func (v *messagesRequest) UnmarshalJSON(data []byte) error {
	type plain messagesRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, requestKnown)
	if err != nil {
		return err
	}
	*v = messagesRequest(p)
	v.Extra = extra
	return nil
}

func (v messagesRequest) MarshalJSON() ([]byte, error) {
	type plain messagesRequest
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *message) UnmarshalJSON(data []byte) error {
	type plain message
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, messageKnown)
	if err != nil {
		return err
	}
	*v = message(p)
	v.Extra = extra
	return nil
}

func (v message) MarshalJSON() ([]byte, error) {
	type plain message
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *contentBlock) UnmarshalJSON(data []byte) error {
	type plain contentBlock
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, blockKnown)
	if err != nil {
		return err
	}
	*v = contentBlock(p)
	v.Extra = extra
	return nil
}

func (v contentBlock) MarshalJSON() ([]byte, error) {
	type plain contentBlock
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *imageSource) UnmarshalJSON(data []byte) error {
	type plain imageSource
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, sourceKnown)
	if err != nil {
		return err
	}
	*v = imageSource(p)
	v.Extra = extra
	return nil
}

func (v imageSource) MarshalJSON() ([]byte, error) {
	type plain imageSource
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *tool) UnmarshalJSON(data []byte) error {
	type plain tool
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolKnown)
	if err != nil {
		return err
	}
	*v = tool(p)
	v.Extra = extra
	return nil
}

func (v tool) MarshalJSON() ([]byte, error) {
	type plain tool
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *messagesResponse) UnmarshalJSON(data []byte) error {
	type plain messagesResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, responseKnown)
	if err != nil {
		return err
	}
	*v = messagesResponse(p)
	v.Extra = extra
	return nil
}

func (v messagesResponse) MarshalJSON() ([]byte, error) {
	type plain messagesResponse
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *usage) UnmarshalJSON(data []byte) error {
	type plain usage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, usageKnown)
	if err != nil {
		return err
	}
	*v = usage(p)
	v.Extra = extra
	return nil
}

func (v usage) MarshalJSON() ([]byte, error) {
	type plain usage
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *streamEvent) UnmarshalJSON(data []byte) error {
	type plain streamEvent
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, eventKnown)
	if err != nil {
		return err
	}
	*v = streamEvent(p)
	v.Extra = extra
	return nil
}

func (v streamEvent) MarshalJSON() ([]byte, error) {
	type plain streamEvent
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *streamMessage) UnmarshalJSON(data []byte) error {
	type plain streamMessage
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, streamMessageKnown)
	if err != nil {
		return err
	}
	*v = streamMessage(p)
	v.Extra = extra
	return nil
}

func (v streamMessage) MarshalJSON() ([]byte, error) {
	type plain streamMessage
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *streamContentBlock) UnmarshalJSON(data []byte) error {
	type plain streamContentBlock
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, streamBlockKnown)
	if err != nil {
		return err
	}
	*v = streamContentBlock(p)
	v.Extra = extra
	return nil
}

func (v streamContentBlock) MarshalJSON() ([]byte, error) {
	type plain streamContentBlock
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *streamDelta) UnmarshalJSON(data []byte) error {
	type plain streamDelta
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, streamDeltaKnown)
	if err != nil {
		return err
	}
	*v = streamDelta(p)
	v.Extra = extra
	return nil
}

func (v streamDelta) MarshalJSON() ([]byte, error) {
	type plain streamDelta
	return marshalWithExtra(plain(v), v.Extra)
}
