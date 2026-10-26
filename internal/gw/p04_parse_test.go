package gw

import (
	"net/http"
	"strings"
	"testing"

	"yunshu.dev/core/internal/apierr"
)

func assertParseError(t *testing.T, endpoint Endpoint, body, code string) {
	t.Helper()
	_, err := ParseRequest(endpoint, []byte(body))
	if err == nil {
		t.Fatalf("want %s for %s", code, body)
	}
	if err.Code != code {
		t.Fatalf("code = %s, want %s (body %s)", err.Code, code, body)
	}
}

func TestP04ChatValidation(t *testing.T) {
	valid := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req, err := ParseRequest(EndpointChat, []byte(valid))
	if err != nil {
		t.Fatalf("valid request rejected: %v (%s)", err, err.Message)
	}
	if req.Model != "m" || len(req.Messages) != 1 || req.Messages[0].Content.Text != "hi" {
		t.Fatalf("ir = %+v", req)
	}

	assertParseError(t, EndpointChat, ``, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointChat, `[]`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointChat, `{"messages":[{"role":"user","content":"hi"}]}`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointChat, `{"model":"","messages":[{"role":"user","content":"hi"}]}`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointChat, `{"model":"m"}`, apierr.CodeInvalidMessages)
	assertParseError(t, EndpointChat, `{"model":"m","messages":[]}`, apierr.CodeInvalidMessages)
	assertParseError(t, EndpointChat, `{"model":"m","messages":[{"role":"wizard","content":"hi"}]}`, apierr.CodeInvalidMessages)
	assertParseError(t, EndpointChat, `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":3}`, apierr.CodeInvalidTemperature)
	assertParseError(t, EndpointChat, `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":40000}`, apierr.CodeMaxTokensExceeded)
	assertParseError(t, EndpointChat, `{"model":"m","messages":[{"role":"user","content":"hi"}],"n":2}`, apierr.CodeInvalidImageCount)
	assertParseError(t, EndpointChat,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"ftp://x"}}]}]}`,
		apierr.CodeInvalidImageURL)
	assertParseError(t, EndpointChat,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`,
		apierr.CodeInvalidTools)
}

func TestP04AnthropicShapes(t *testing.T) {
	body := `{"model":"m","system":[{"type":"text","text":"be brief"}],"max_tokens":100,
		"stop_sequences":["END"],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`
	req, err := ParseRequest(EndpointMessages, []byte(body))
	if err != nil {
		t.Fatalf("anthropic request rejected: %v (%s)", err, err.Message)
	}
	if req.System != "be brief\n" {
		t.Fatalf("system = %q", req.System)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "END" {
		t.Fatalf("stop = %v", req.Stop)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "lookup" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 100 {
		t.Fatalf("max_tokens = %v", req.MaxTokens)
	}
}

func TestP04ImageValidation(t *testing.T) {
	valid := `{"model":"img","prompt":"a cat","n":2,"size":"512x512"}`
	req, err := ParseRequest(EndpointImages, []byte(valid))
	if err != nil {
		t.Fatalf("valid image request rejected: %v", err)
	}
	if req.N != 2 || req.Size != "512x512" || req.ImageParams == nil {
		t.Fatalf("ir = %+v", req)
	}

	assertParseError(t, EndpointImages, `{"model":"img"}`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointImages, `{"model":"img","prompt":""}`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointImages, `{"model":"img","prompt":"x","n":5}`, apierr.CodeInvalidImageCount)
	assertParseError(t, EndpointImages, `{"model":"img","prompt":"x","n":0}`, apierr.CodeInvalidImageCount)
	assertParseError(t, EndpointImages, `{"model":"img","prompt":"x","size":"100x100"}`, apierr.CodeInvalidImageSize)
}

func TestP04EmbeddingsValidation(t *testing.T) {
	req, err := ParseRequest(EndpointEmbeddings, []byte(`{"model":"e","input":"hello"}`))
	if err != nil || len(req.EmbedInputs) != 1 {
		t.Fatalf("single input: %+v, %v", req, err)
	}
	req, err = ParseRequest(EndpointEmbeddings, []byte(`{"model":"e","input":["a","b"]}`))
	if err != nil || len(req.EmbedInputs) != 2 {
		t.Fatalf("array input: %+v, %v", req, err)
	}
	req, err = ParseRequest(EndpointEmbeddings, []byte(`{"model":"e","input":[[1,2],[3]]}`))
	if err != nil {
		t.Fatalf("token arrays: %v", err)
	}
	assertParseError(t, EndpointEmbeddings, `{"model":"e"}`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointEmbeddings, `{"model":"e","input":1}`, apierr.CodeInvalidRequestBody)
	assertParseError(t, EndpointEmbeddings, `{"model":"e","input":[]}`, apierr.CodeInvalidRequestBody)
}

func TestP04KeepsUnknownFieldsForPassthrough(t *testing.T) {
	raw := `{"model":"m","messages":[{"role":"user","content":"hi"}],"enable_search":true,"bogus":{"a":1}}`
	req, err := ParseRequest(EndpointChat, []byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := req.RawExtra["enable_search"]; !ok {
		t.Fatalf("raw_extra = %v; unknown upstream extension was dropped", req.RawExtra)
	}
	if _, ok := req.RawExtra["bogus"]; !ok {
		t.Fatalf("raw_extra = %v", req.RawExtra)
	}
}

func TestP03UnknownPathAndWrongMethod(t *testing.T) {
	cases := []struct {
		method string
		path   string
		ok     bool
	}{
		{http.MethodPost, "/v1/chat/completions", true},
		{http.MethodGet, "/v1/models", true},
		{http.MethodGet, "/v1/chat/completions", false},
		{http.MethodPost, "/v1/models", false},
		{http.MethodGet, "/v1/nope", false},
		{http.MethodPost, "/v1/embeddings/extra", false},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, tc.path, strings.NewReader(""))
		_, _, err := MatchEndpoint(req)
		if tc.ok && err != nil {
			t.Fatalf("%s %s rejected: %s", tc.method, tc.path, err.Code)
		}
		if !tc.ok {
			if err == nil || err.Code != apierr.CodeUnknownEndpoint {
				t.Fatalf("%s %s: %v, want unknown_endpoint", tc.method, tc.path, err)
			}
		}
	}
}
