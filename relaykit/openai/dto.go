package openai

import "encoding/json"

// Wire shapes for the OpenAI-compatible dialect. They are deliberately local to
// relaykit: no business type from internal/ may leak into the conversion layer.
//
// Every struct carries Extra for members this adapter does not model. The
// custom Marshal/Unmarshal pair merges Extra at the same nesting level, so an
// unknown key survives both directions instead of being dropped silently.

type chatRequest struct {
	Model         string                     `json:"model"`
	Messages      []message                  `json:"messages"`
	Stream        bool                       `json:"stream,omitempty"`
	StreamOptions *streamOptions             `json:"stream_options,omitempty"`
	MaxTokens     *int64                     `json:"max_tokens,omitempty"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	Stop          json.RawMessage            `json:"stop,omitempty"`
	Tools         []tool                     `json:"tools,omitempty"`
	ToolChoice    json.RawMessage            `json:"tool_choice,omitempty"`
	ResponseFmt   json.RawMessage            `json:"response_format,omitempty"`
	N             int                        `json:"n,omitempty"`
	System        string                     `json:"system,omitempty"`
	Extra         map[string]json.RawMessage `json:"-"`
}

type streamOptions struct {
	IncludeUsage bool                       `json:"include_usage"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type message struct {
	Role       string                     `json:"role"`
	Content    json.RawMessage            `json:"content,omitempty"`
	Name       string                     `json:"name,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall                 `json:"tool_calls,omitempty"`
	Extra      map[string]json.RawMessage `json:"-"`
}

type contentPart struct {
	Type     string                     `json:"type"`
	Text     string                     `json:"text,omitempty"`
	ImageURL *imageURL                  `json:"image_url,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type imageURL struct {
	URL    string                     `json:"url"`
	Detail string                     `json:"detail,omitempty"`
	Extra  map[string]json.RawMessage `json:"-"`
}

type toolCall struct {
	ID       string                     `json:"id,omitempty"`
	Type     string                     `json:"type,omitempty"`
	Index    *int                       `json:"index,omitempty"`
	Function toolFunctionCall           `json:"function"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type toolFunctionCall struct {
	Name      string                     `json:"name,omitempty"`
	Arguments string                     `json:"arguments,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type tool struct {
	Type     string                     `json:"type,omitempty"`
	Function toolFunction               `json:"function"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type toolFunction struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  json.RawMessage            `json:"parameters,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

type chatResponse struct {
	ID      string                     `json:"id"`
	Object  string                     `json:"object"`
	Model   string                     `json:"model"`
	Created int64                      `json:"created"`
	Choices []choice                   `json:"choices"`
	Usage   *usage                     `json:"usage,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type choice struct {
	Index        int                        `json:"index"`
	Message      *message                   `json:"message,omitempty"`
	Delta        *delta                     `json:"delta,omitempty"`
	FinishReason any                        `json:"finish_reason"`
	Logprobs     json.RawMessage            `json:"logprobs,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type delta struct {
	Role      string                     `json:"role,omitempty"`
	Content   string                     `json:"content,omitempty"`
	ToolCalls []toolCall                 `json:"tool_calls,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type usage struct {
	PromptTokens            int64                      `json:"prompt_tokens"`
	CompletionTokens        int64                      `json:"completion_tokens"`
	TotalTokens             int64                      `json:"total_tokens"`
	PromptTokensDetails     *promptTokensDetails       `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *completionTokensDetails   `json:"completion_tokens_details,omitempty"`
	Extra                   map[string]json.RawMessage `json:"-"`
}

type promptTokensDetails struct {
	CachedTokens int64                      `json:"cached_tokens,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type completionTokensDetails struct {
	ReasoningTokens int64                      `json:"reasoning_tokens,omitempty"`
	Extra           map[string]json.RawMessage `json:"-"`
}

type embeddingsRequest struct {
	Model          string                     `json:"model"`
	Input          json.RawMessage            `json:"input"`
	EncodingFormat string                     `json:"encoding_format,omitempty"`
	Extra          map[string]json.RawMessage `json:"-"`
}

type imageRequest struct {
	Model          string                     `json:"model"`
	Prompt         string                     `json:"prompt"`
	N              int                        `json:"n,omitempty"`
	Size           string                     `json:"size,omitempty"`
	ResponseFormat string                     `json:"response_format,omitempty"`
	Extra          map[string]json.RawMessage `json:"-"`
}

var chatRequestKnown = knownSet("model", "messages", "stream", "stream_options",
	"max_tokens", "temperature", "top_p", "stop", "tools", "tool_choice",
	"response_format", "n", "system")
var streamOptionsKnown = knownSet("include_usage")
var messageKnown = knownSet("role", "content", "name", "tool_call_id", "tool_calls")
var contentPartKnown = knownSet("type", "text", "image_url")
var imageURLKnown = knownSet("url", "detail")
var toolCallKnown = knownSet("id", "type", "index", "function")
var toolFunctionCallKnown = knownSet("name", "arguments")
var toolKnown = knownSet("type", "function")
var toolFunctionKnown = knownSet("name", "description", "parameters")
var chatResponseKnown = knownSet("id", "object", "model", "created", "choices", "usage")
var choiceKnown = knownSet("index", "message", "delta", "finish_reason", "logprobs")
var deltaKnown = knownSet("role", "content", "tool_calls")
var usageKnown = knownSet("prompt_tokens", "completion_tokens", "total_tokens",
	"prompt_tokens_details", "completion_tokens_details")
var promptDetailsKnown = knownSet("cached_tokens")
var completionDetailsKnown = knownSet("reasoning_tokens")
var embeddingsKnown = knownSet("model", "input", "encoding_format")
var imageKnown = knownSet("model", "prompt", "n", "size", "response_format")

func (v *chatRequest) UnmarshalJSON(data []byte) error {
	type plain chatRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, chatRequestKnown)
	if err != nil {
		return err
	}
	*v = chatRequest(p)
	v.Extra = extra
	return nil
}

func (v chatRequest) MarshalJSON() ([]byte, error) {
	type plain chatRequest
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *streamOptions) UnmarshalJSON(data []byte) error {
	type plain streamOptions
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, streamOptionsKnown)
	if err != nil {
		return err
	}
	*v = streamOptions(p)
	v.Extra = extra
	return nil
}

func (v streamOptions) MarshalJSON() ([]byte, error) {
	type plain streamOptions
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

func (v *contentPart) UnmarshalJSON(data []byte) error {
	type plain contentPart
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, contentPartKnown)
	if err != nil {
		return err
	}
	*v = contentPart(p)
	v.Extra = extra
	return nil
}

func (v contentPart) MarshalJSON() ([]byte, error) {
	type plain contentPart
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *imageURL) UnmarshalJSON(data []byte) error {
	type plain imageURL
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, imageURLKnown)
	if err != nil {
		return err
	}
	*v = imageURL(p)
	v.Extra = extra
	return nil
}

func (v imageURL) MarshalJSON() ([]byte, error) {
	type plain imageURL
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *toolCall) UnmarshalJSON(data []byte) error {
	type plain toolCall
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolCallKnown)
	if err != nil {
		return err
	}
	*v = toolCall(p)
	v.Extra = extra
	return nil
}

func (v toolCall) MarshalJSON() ([]byte, error) {
	type plain toolCall
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *toolFunctionCall) UnmarshalJSON(data []byte) error {
	type plain toolFunctionCall
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolFunctionCallKnown)
	if err != nil {
		return err
	}
	*v = toolFunctionCall(p)
	v.Extra = extra
	return nil
}

func (v toolFunctionCall) MarshalJSON() ([]byte, error) {
	type plain toolFunctionCall
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

func (v *toolFunction) UnmarshalJSON(data []byte) error {
	type plain toolFunction
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, toolFunctionKnown)
	if err != nil {
		return err
	}
	*v = toolFunction(p)
	v.Extra = extra
	return nil
}

func (v toolFunction) MarshalJSON() ([]byte, error) {
	type plain toolFunction
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *chatResponse) UnmarshalJSON(data []byte) error {
	type plain chatResponse
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, chatResponseKnown)
	if err != nil {
		return err
	}
	*v = chatResponse(p)
	v.Extra = extra
	return nil
}

func (v chatResponse) MarshalJSON() ([]byte, error) {
	type plain chatResponse
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *choice) UnmarshalJSON(data []byte) error {
	type plain choice
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, choiceKnown)
	if err != nil {
		return err
	}
	*v = choice(p)
	v.Extra = extra
	return nil
}

func (v choice) MarshalJSON() ([]byte, error) {
	type plain choice
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *delta) UnmarshalJSON(data []byte) error {
	type plain delta
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, deltaKnown)
	if err != nil {
		return err
	}
	*v = delta(p)
	v.Extra = extra
	return nil
}

func (v delta) MarshalJSON() ([]byte, error) {
	type plain delta
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

func (v *promptTokensDetails) UnmarshalJSON(data []byte) error {
	type plain promptTokensDetails
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, promptDetailsKnown)
	if err != nil {
		return err
	}
	*v = promptTokensDetails(p)
	v.Extra = extra
	return nil
}

func (v promptTokensDetails) MarshalJSON() ([]byte, error) {
	type plain promptTokensDetails
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *completionTokensDetails) UnmarshalJSON(data []byte) error {
	type plain completionTokensDetails
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, completionDetailsKnown)
	if err != nil {
		return err
	}
	*v = completionTokensDetails(p)
	v.Extra = extra
	return nil
}

func (v completionTokensDetails) MarshalJSON() ([]byte, error) {
	type plain completionTokensDetails
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *embeddingsRequest) UnmarshalJSON(data []byte) error {
	type plain embeddingsRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, embeddingsKnown)
	if err != nil {
		return err
	}
	*v = embeddingsRequest(p)
	v.Extra = extra
	return nil
}

func (v embeddingsRequest) MarshalJSON() ([]byte, error) {
	type plain embeddingsRequest
	return marshalWithExtra(plain(v), v.Extra)
}

func (v *imageRequest) UnmarshalJSON(data []byte) error {
	type plain imageRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	extra, err := unknownMembers(data, imageKnown)
	if err != nil {
		return err
	}
	*v = imageRequest(p)
	v.Extra = extra
	return nil
}

func (v imageRequest) MarshalJSON() ([]byte, error) {
	type plain imageRequest
	return marshalWithExtra(plain(v), v.Extra)
}
