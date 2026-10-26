package gw

import (
	"strconv"
	"strings"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/jsonx"
)

// PlatformMaxOutputTokens is the P04 platform ceiling on max_tokens (TDS
// §3.1 E-23). The route-level max_tokens_ceiling is a separate check in P12.
const PlatformMaxOutputTokens = 32_768

// imageSizes is the accepted size set (TDS §3.1 field table).
var imageSizes = map[string]bool{
	"512x512":   true,
	"768x768":   true,
	"1024x1024": true,
	"1024x1792": true,
	"1792x1024": true,
}

// knownFields lists every top-level member the IR understands. Everything
// else is preserved in RawExtra so P14 can pass it through upstream.
var knownFields = map[string]bool{
	"model": true, "messages": true, "stream": true, "max_tokens": true,
	"temperature": true, "top_p": true, "stop": true, "stop_sequences": true,
	"tools": true, "tool_choice": true, "response_format": true, "n": true,
	"system": true, "prompt": true, "size": true, "input": true,
	"encoding_format": true, "metadata": true,
}

// ParseRequest performs P04. It validates shape only: model existence,
// listing state and authorization are P07, and business authorization must not
// leak into this stage (card §4.16).
func ParseRequest(endpoint Endpoint, body []byte) (*IRRequest, *apierr.APIError) {
	if len(body) == 0 {
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody, "请求体为空")
	}
	var fields map[string]jsonx.RawMessage
	if err := jsonx.Unmarshal(body, &fields); err != nil {
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody, "无法解析为 JSON 对象")
	}
	if fields == nil {
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody, "无法解析为 JSON 对象")
	}

	req := &IRRequest{RawExtra: map[string]jsonx.RawMessage{}}
	for name, raw := range fields {
		if !knownFields[name] {
			req.RawExtra[name] = raw
		}
	}

	if err := parseModel(fields, req); err != nil {
		return nil, err
	}
	if err := parseStream(fields, req); err != nil {
		return nil, err
	}

	switch endpoint {
	case EndpointChat, EndpointMessages:
		if err := parseMessages(fields, req); err != nil {
			return nil, err
		}
		if err := parseSystem(fields, req); err != nil {
			return nil, err
		}
		if err := parseTools(fields, req); err != nil {
			return nil, err
		}
	case EndpointImages:
		if err := parseImage(fields, req); err != nil {
			return nil, err
		}
	case EndpointEmbeddings:
		if err := parseEmbeddings(fields, req); err != nil {
			return nil, err
		}
	}

	if err := parseGeneration(fields, req, endpoint); err != nil {
		return nil, err
	}
	return req, nil
}

func parseModel(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	raw, ok := fields["model"]
	if !ok {
		return apierr.Newf(apierr.CodeInvalidRequestBody, "缺少 model")
	}
	var model string
	if err := jsonx.Unmarshal(raw, &model); err != nil {
		return apierr.Newf(apierr.CodeInvalidRequestBody, "model 必须是字符串")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return apierr.Newf(apierr.CodeInvalidRequestBody, "model 不能为空")
	}
	req.Model = model
	return nil
}

func parseStream(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	raw, ok := fields["stream"]
	if !ok {
		return nil
	}
	var stream bool
	if err := jsonx.Unmarshal(raw, &stream); err != nil {
		return apierr.Newf(apierr.CodeInvalidRequestBody, "stream 必须是布尔值")
	}
	req.Stream = stream
	return nil
}

type wireMessage struct {
	Role    string           `json:"role"`
	Content jsonx.RawMessage `json:"content"`
	Name    string           `json:"name"`
}

func parseMessages(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	raw, ok := fields["messages"]
	if !ok {
		return apierr.New(apierr.CodeInvalidMessages)
	}
	var items []jsonx.RawMessage
	if err := jsonx.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return apierr.New(apierr.CodeInvalidMessages)
	}
	for _, item := range items {
		var wire wireMessage
		if err := jsonx.Unmarshal(item, &wire); err != nil {
			return apierr.New(apierr.CodeInvalidMessages)
		}
		role := strings.ToLower(strings.TrimSpace(wire.Role))
		switch role {
		case "system", "user", "assistant", "tool":
		default:
			return apierr.New(apierr.CodeInvalidMessages)
		}
		content, err := parseContent(wire.Content)
		if err != nil {
			return err
		}
		if role != "assistant" && content.Text == "" && len(content.Parts) == 0 {
			return apierr.New(apierr.CodeInvalidMessages)
		}
		req.Messages = append(req.Messages, IRMessage{
			Role:    role,
			Name:    wire.Name,
			Content: content,
		})
	}
	return nil
}

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func parseContent(raw jsonx.RawMessage) (IRContent, *apierr.APIError) {
	if len(raw) == 0 {
		return IRContent{}, nil
	}
	var text string
	if err := jsonx.Unmarshal(raw, &text); err == nil {
		return IRContent{Text: text}, nil
	}
	var parts []wirePart
	if err := jsonx.Unmarshal(raw, &parts); err != nil {
		return IRContent{}, apierr.New(apierr.CodeInvalidMessages)
	}
	out := IRContent{Parts: make([]IRContentPart, 0, len(parts))}
	for _, p := range parts {
		part := IRContentPart{Type: p.Type, Text: p.Text}
		if p.Type == "image_url" {
			if p.ImageURL == nil || !validImageURL(p.ImageURL.URL) {
				return IRContent{}, apierr.New(apierr.CodeInvalidImageURL)
			}
			part.ImageURL = p.ImageURL.URL
		}
		if p.Type != "text" && p.Type != "image_url" &&
			p.Type != "tool_use" && p.Type != "tool_result" {
			return IRContent{}, apierr.New(apierr.CodeInvalidMessages)
		}
		out.Parts = append(out.Parts, part)
	}
	return out, nil
}

func validImageURL(raw string) bool {
	return strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "data:image/")
}

func parseSystem(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	raw, ok := fields["system"]
	if !ok {
		return nil
	}
	var text string
	if err := jsonx.Unmarshal(raw, &text); err == nil {
		req.System = text
		return nil
	}
	var parts []wirePart
	if err := jsonx.Unmarshal(raw, &parts); err != nil {
		return apierr.New(apierr.CodeInvalidMessages)
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return apierr.New(apierr.CodeInvalidMessages)
		}
		b.WriteString(p.Text)
		b.WriteByte('\n')
	}
	req.System = b.String()
	return nil
}

type wireTool struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Function *struct {
		Name       string           `json:"name"`
		Parameters jsonx.RawMessage `json:"parameters"`
	} `json:"function"`
	InputSchema jsonx.RawMessage `json:"input_schema"`
}

func parseTools(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	raw, ok := fields["tools"]
	if !ok {
		return nil
	}
	var items []wireTool
	if err := jsonx.Unmarshal(raw, &items); err != nil {
		return apierr.Newf(apierr.CodeInvalidTools, "tools[?]")
	}
	for i, item := range items {
		tool := IRTool{}
		switch {
		case item.Function != nil && item.Type == "function":
			tool.Name = strings.TrimSpace(item.Function.Name)
			tool.Parameters = item.Function.Parameters
		case strings.TrimSpace(item.Name) != "":
			// Anthropic shape: {name, description, input_schema}.
			tool.Name = strings.TrimSpace(item.Name)
			tool.Parameters = item.InputSchema
		default:
			return apierr.Newf(apierr.CodeInvalidTools, "tools["+strconv.Itoa(i)+"]")
		}
		req.Tools = append(req.Tools, tool)
	}
	return nil
}

func parseImage(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	var prompt string
	if raw, ok := fields["prompt"]; ok {
		if err := jsonx.Unmarshal(raw, &prompt); err != nil || strings.TrimSpace(prompt) == "" {
			return apierr.Newf(apierr.CodeInvalidRequestBody, "prompt 不能为空")
		}
	} else {
		return apierr.Newf(apierr.CodeInvalidRequestBody, "缺少 prompt")
	}
	size := "1024x1024"
	if raw, ok := fields["size"]; ok {
		if err := jsonx.Unmarshal(raw, &size); err != nil {
			return apierr.Newf(apierr.CodeInvalidImageSize, size)
		}
		if !imageSizes[size] {
			return apierr.Newf(apierr.CodeInvalidImageSize, size)
		}
	}
	responseFormat := "url"
	if raw, ok := fields["response_format"]; ok {
		_ = jsonx.Unmarshal(raw, &responseFormat)
	}
	n := 1
	if raw, ok := fields["n"]; ok {
		if err := jsonx.Unmarshal(raw, &n); err != nil {
			return apierr.Newf(apierr.CodeInvalidImageCount, 0)
		}
	}
	if n < 1 || n > 4 {
		return apierr.Newf(apierr.CodeInvalidImageCount, n)
	}
	req.Prompt = prompt
	req.N = n
	req.Size = size
	req.ResponseFormat = responseFormat
	req.ImageParams = &IRImageParams{
		Prompt: prompt, N: n, Size: size, ResponseFormat: responseFormat,
	}
	return nil
}

func parseEmbeddings(fields map[string]jsonx.RawMessage, req *IRRequest) *apierr.APIError {
	raw, ok := fields["input"]
	if !ok {
		return apierr.Newf(apierr.CodeInvalidRequestBody, "缺少 input")
	}
	var single string
	if err := jsonx.Unmarshal(raw, &single); err == nil {
		req.EmbedInputs = []string{single}
		return nil
	}
	var many []string
	if err := jsonx.Unmarshal(raw, &many); err == nil {
		if len(many) == 0 {
			return apierr.Newf(apierr.CodeInvalidRequestBody, "input 不能为空数组")
		}
		req.EmbedInputs = many
		return nil
	}
	// Token arrays (token[][] ) are accepted untouched; they cannot be counted
	// by the local tokenizer, which yields a conservative zero input count.
	var tokenArrays []jsonx.RawMessage
	if err := jsonx.Unmarshal(raw, &tokenArrays); err == nil {
		req.EmbedInputs = []string{}
		return nil
	}
	return apierr.Newf(apierr.CodeInvalidRequestBody, "input 类型不支持")
}

func parseGeneration(fields map[string]jsonx.RawMessage, req *IRRequest, endpoint Endpoint) *apierr.APIError {
	if raw, ok := fields["temperature"]; ok {
		var temp float64
		if err := jsonx.Unmarshal(raw, &temp); err != nil {
			return apierr.Newf(apierr.CodeInvalidTemperature, "非数值")
		}
		if temp < 0 || temp > 2 {
			return apierr.Newf(apierr.CodeInvalidTemperature, temp)
		}
		req.Temperature = &temp
	}
	if raw, ok := fields["top_p"]; ok {
		var topP float64
		if err := jsonx.Unmarshal(raw, &topP); err == nil {
			req.TopP = &topP
		}
	}
	if raw, ok := fields["max_tokens"]; ok {
		var maxTokens int64
		if err := jsonx.Unmarshal(raw, &maxTokens); err != nil {
			return apierr.Newf(apierr.CodeInvalidRequestBody, "max_tokens 必须是整数")
		}
		if maxTokens < 1 || maxTokens > PlatformMaxOutputTokens {
			return apierr.Newf(apierr.CodeMaxTokensExceeded, PlatformMaxOutputTokens, maxTokens)
		}
		req.MaxTokens = &maxTokens
	}
	if raw, ok := fields["n"]; ok && endpoint != EndpointImages {
		var n int
		if err := jsonx.Unmarshal(raw, &n); err != nil {
			return apierr.Newf(apierr.CodeInvalidImageCount, 0)
		}
		// TEXT models generate exactly one choice (TDS §3.1 P04 table).
		if n != 1 {
			return apierr.Newf(apierr.CodeInvalidImageCount, n)
		}
		req.N = n
	}
	if raw, ok := fields["stop"]; ok {
		var single string
		if err := jsonx.Unmarshal(raw, &single); err == nil {
			req.Stop = []string{single}
		} else {
			var many []string
			if err := jsonx.Unmarshal(raw, &many); err == nil {
				req.Stop = many
			}
		}
	}
	if raw, ok := fields["stop_sequences"]; ok {
		var many []string
		if err := jsonx.Unmarshal(raw, &many); err == nil {
			req.Stop = many
		}
	}
	if raw, ok := fields["response_format"]; ok {
		var shape struct {
			Type string `json:"type"`
		}
		if err := jsonx.Unmarshal(raw, &shape); err == nil {
			req.ResponseFormat = shape.Type
		}
	}
	if raw, ok := fields["tool_choice"]; ok {
		req.ToolChoice = raw
	}
	return nil
}
