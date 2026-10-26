package relaykit_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yunshu.dev/relaykit/anthropic"
	"yunshu.dev/relaykit/bailian"
	"yunshu.dev/relaykit/ir"
	"yunshu.dev/relaykit/openai"
)

type requestCodec struct {
	name   string
	decode func([]byte) (*ir.Request, error)
	encode func(*ir.Request) ([]byte, error)
	full   string
	common string
}

type responseCodec struct {
	name   string
	decode func([]byte) (*ir.Response, error)
	encode func(*ir.Response) ([]byte, error)
	full   string
	common string
}

var requestCodecs = []requestCodec{
	{"openai", openai.DecodeChatRequest, openai.EncodeChatRequest,
		"openai_request_full.json", "openai_request_common.json"},
	{"anthropic", anthropic.DecodeRequest, anthropic.EncodeRequest,
		"anthropic_request_full.json", "anthropic_request_common.json"},
	{"bailian", bailian.DecodeRequest, bailian.EncodeRequest,
		"bailian_request_full.json", "bailian_request_common.json"},
}

var responseCodecs = []responseCodec{
	{"openai", openai.DecodeResponse, openai.EncodeResponse,
		"openai_response_full.json", "openai_response_common.json"},
	{"anthropic", anthropic.DecodeResponse, anthropic.EncodeResponse,
		"anthropic_response_full.json", "anthropic_response_common.json"},
	{"bailian", bailian.DecodeResponse, bailian.EncodeResponse,
		"bailian_response_full.json", "bailian_response_common.json"},
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// TestGoldenRequestSameProtocolRoundTrip is the lossless half of A2: within one
// protocol every modeled field and every Extra member must survive exactly.
func TestGoldenRequestSameProtocolRoundTrip(t *testing.T) {
	for _, codec := range requestCodecs {
		t.Run(codec.name, func(t *testing.T) {
			first, err := codec.decode(fixture(t, codec.full))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			body, err := codec.encode(first)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			second, err := codec.decode(body)
			if err != nil {
				t.Fatalf("decode again: %v\nbody=%s", err, body)
			}
			// RawMessage members are compared through their JSON value, not
			// byte-for-byte: the first decode keeps fixture whitespace, the
			// second keeps the compactor's output. The value is what matters.
			if !reflect.DeepEqual(semanticJSON(t, first), semanticJSON(t, second)) {
				t.Fatalf("IR changed across one %s round trip\nfirst:  %s\nsecond: %s\nbody: %s",
					codec.name, dump(first), dump(second), body)
			}
		})
	}
}

// TestGoldenResponseSameProtocolRoundTrip is the response half of A2.
func TestGoldenResponseSameProtocolRoundTrip(t *testing.T) {
	for _, codec := range responseCodecs {
		t.Run(codec.name, func(t *testing.T) {
			first, err := codec.decode(fixture(t, codec.full))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			body, err := codec.encode(first)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			second, err := codec.decode(body)
			if err != nil {
				t.Fatalf("decode again: %v\nbody=%s", err, body)
			}
			if !reflect.DeepEqual(semanticJSON(t, first), semanticJSON(t, second)) {
				t.Fatalf("IR changed across one %s response round trip\nfirst:  %s\nsecond: %s\nbody: %s",
					codec.name, dump(first), dump(second), body)
			}
		})
	}
}

// TestThreeRoundTripsAreSemanticallyEquivalent converts each common fixture
// through the other two protocols and back to the start (three hops). The
// comparison deliberately drops protocol-specific Extra members: logprobs only
// exists on OpenAI, metadata/user_id only on Anthropic, enable_search only on
// Bailian. Their preservation is a passthrough property asserted separately by
// TestUnknownField*; folding them into this semantic comparison would only
// assert that a vendor key survived, not that the protocol-neutral meaning did.
// The response envelope (object/type, created) is also excluded: each encoder
// must emit its own protocol's envelope, so IR.Object and IR.Created are not
// transportable across protocols.
func TestThreeRoundTripsAreSemanticallyEquivalent(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		for start := range requestCodecs {
			t.Run(requestCodecs[start].name, func(t *testing.T) {
				initial := decodeRequest(t, requestCodecs[start], fixture(t, requestCodecs[start].common))
				final := cycleRequest(t, start, requestCodecs[start].common, 3)
				want, got := canonicalRequest(initial), canonicalRequest(final)
				if !reflect.DeepEqual(want, got) {
					t.Fatalf("semantic drift after three hops\nwant: %+v\ngot:  %+v", want, got)
				}
			})
		}
	})

	t.Run("response", func(t *testing.T) {
		for start := range responseCodecs {
			t.Run(responseCodecs[start].name, func(t *testing.T) {
				initial := decodeResponse(t, responseCodecs[start], fixture(t, responseCodecs[start].common))
				final := cycleResponse(t, start, 3)
				want, got := canonicalResponse(initial), canonicalResponse(final)
				if !reflect.DeepEqual(want, got) {
					t.Fatalf("semantic drift after three hops\nwant: %+v\ngot:  %+v", want, got)
				}
			})
		}
	})
}

func decodeRequest(t *testing.T, codec requestCodec, body []byte) *ir.Request {
	t.Helper()
	req, err := codec.decode(body)
	if err != nil {
		t.Fatalf("%s decode: %v", codec.name, err)
	}
	return req
}

func decodeResponse(t *testing.T, codec responseCodec, body []byte) *ir.Response {
	t.Helper()
	resp, err := codec.decode(body)
	if err != nil {
		t.Fatalf("%s decode: %v", codec.name, err)
	}
	return resp
}

func cycleRequest(t *testing.T, start int, fixtureName string, hops int) *ir.Request {
	t.Helper()
	req := decodeRequest(t, requestCodecs[start], fixture(t, fixtureName))
	for hop := 1; hop <= hops; hop++ {
		next := requestCodecs[(start+hop)%len(requestCodecs)]
		// A hop is IR -> destination wire -> IR. Encoding with the origin and
		// decoding with the destination would be protocol-to-protocol conversion,
		// which the relay never performs.
		body, err := next.encode(req)
		if err != nil {
			t.Fatalf("%s encode at hop %d: %v", next.name, hop, err)
		}
		req = decodeRequest(t, next, body)
	}
	return req
}

func cycleResponse(t *testing.T, start, hops int) *ir.Response {
	t.Helper()
	resp := decodeResponse(t, responseCodecs[start], fixture(t, responseCodecs[start].common))
	for hop := 1; hop <= hops; hop++ {
		next := responseCodecs[(start+hop)%len(responseCodecs)]
		body, err := next.encode(resp)
		if err != nil {
			t.Fatalf("%s encode at hop %d: %v", next.name, hop, err)
		}
		resp = decodeResponse(t, next, body)
	}
	return resp
}

// TestUnknownFieldLogprobsSurvives asserts an OpenAI-only member survives every
// protocol hop, even though the other adapters do not model it.
func TestUnknownFieldLogprobsSurvives(t *testing.T) {
	req := decodeRequest(t, requestCodecs[0], fixture(t, "openai_request_common.json"))
	assertRawTrue(t, "initial logprobs", req.RawExtra["logprobs"])
	for hop, codec := range requestCodecs[1:] {
		body, err := codec.encode(req)
		if err != nil {
			t.Fatalf("hop %d %s encode: %v", hop, codec.name, err)
		}
		req, err = codec.decode(body)
		if err != nil {
			t.Fatalf("hop %d %s decode: %v", hop, codec.name, err)
		}
		assertRawTrue(t, "logprobs after "+codec.name, req.RawExtra["logprobs"])
		assertJSONNumber(t, "top_logprobs after "+codec.name, req.RawExtra["top_logprobs"], 3)
	}
}

// TestUnknownFieldMetadataUserIDSurvives asserts metadata.user_id, an
// Anthropic-only member, survives the other two protocols.
func TestUnknownFieldMetadataUserIDSurvives(t *testing.T) {
	req := decodeRequest(t, requestCodecs[1], fixture(t, "anthropic_request_common.json"))
	assertMetadataUser(t, "initial", req.RawExtra["metadata"])
	for _, codec := range []requestCodec{requestCodecs[2], requestCodecs[0], requestCodecs[1]} {
		body, err := codec.encode(req)
		if err != nil {
			t.Fatalf("%s encode: %v", codec.name, err)
		}
		req, err = codec.decode(body)
		if err != nil {
			t.Fatalf("%s decode: %v", codec.name, err)
		}
		assertMetadataUser(t, "after "+codec.name, req.RawExtra["metadata"])
	}
}

// TestUnknownFieldBailianEnableSearchSurvives asserts the vendor extension
// enable_search is not swallowed by the OpenAI or Anthropic leg.
func TestUnknownFieldBailianEnableSearchSurvives(t *testing.T) {
	req := decodeRequest(t, requestCodecs[2], fixture(t, "bailian_request_common.json"))
	assertRawTrue(t, "initial enable_search", req.RawExtra["enable_search"])
	for _, codec := range []requestCodec{requestCodecs[0], requestCodecs[1], requestCodecs[2]} {
		body, err := codec.encode(req)
		if err != nil {
			t.Fatalf("%s encode: %v", codec.name, err)
		}
		req, err = codec.decode(body)
		if err != nil {
			t.Fatalf("%s decode: %v", codec.name, err)
		}
		assertRawTrue(t, "enable_search after "+codec.name, req.RawExtra["enable_search"])
	}
}

// TestChunkContentFlagIsTheOnlyFirstTokenSignal encodes the T-26 rule: role
// frames, empty deltas, usage frames and terminal frames carry HasContent =
// false; only a non-empty content increment sets it.
func TestChunkContentFlagIsTheOnlyFirstTokenSignal(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"role", `{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`, false},
		{"empty delta", `{"choices":[{"index":0,"delta":{},"finish_reason":null}]}`, false},
		{"content", `{"choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}`, true},
		{"tool call", `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f"}}]},"finish_reason":null}]}`, false},
		{"usage only", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunk, err := openai.DecodeChunk([]byte(tc.data))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if chunk.HasContent != tc.want {
				t.Fatalf("HasContent = %v, want %v", chunk.HasContent, tc.want)
			}
		})
	}
}

type sseFrame struct {
	Name  string          `json:"name"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func loadFrames(t *testing.T, name string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	if err := json.Unmarshal(fixture(t, name), &frames); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return frames
}

func frameByName(t *testing.T, frames []sseFrame, name string) sseFrame {
	t.Helper()
	for _, frame := range frames {
		if frame.Name == name {
			return frame
		}
	}
	t.Fatalf("frame %q not found", name)
	return sseFrame{}
}

// framePayload returns the wire payload of a fixture frame. Fixture values are
// JSON so "[DONE]" is stored as a JSON string, while the SSE wire carries the
// bare token; unquote string values before handing them to a decoder.
func framePayload(t *testing.T, frame sseFrame) []byte {
	t.Helper()
	var text string
	if err := json.Unmarshal(frame.Data, &text); err == nil {
		return []byte(text)
	}
	return frame.Data
}

func decodeOpenAIFrame(t *testing.T, frame sseFrame) *ir.Chunk {
	t.Helper()
	chunk, err := openai.DecodeChunk(framePayload(t, frame))
	if err != nil {
		t.Fatalf("%s: %v", frame.Name, err)
	}
	return chunk
}

func TestOpenAIStreamFramesDecodeToIR(t *testing.T) {
	frames := loadFrames(t, "openai_sse_frames.json")

	role := decodeOpenAIFrame(t, frameByName(t, frames, "role"))
	if role.HasContent || role.Role != "assistant" {
		t.Fatalf("role frame = %+v", role)
	}
	content := decodeOpenAIFrame(t, frameByName(t, frames, "content"))
	if !content.HasContent || content.Choices[0].Delta.Content != "你" {
		t.Fatalf("content frame = %+v", content)
	}
	if empty := decodeOpenAIFrame(t, frameByName(t, frames, "empty-delta")); empty.HasContent {
		t.Fatalf("empty delta marked as content: %+v", empty)
	}
	tool := decodeOpenAIFrame(t, frameByName(t, frames, "tool"))
	if len(tool.Choices[0].Delta.ToolCalls) != 1 || tool.Choices[0].Delta.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool frame = %+v", tool)
	}
	if tool.HasContent {
		t.Fatalf("tool call frame must not count as content")
	}
	usage := decodeOpenAIFrame(t, frameByName(t, frames, "usage"))
	if usage.Usage == nil || usage.Usage.CacheReadTokens != 3 || usage.HasContent {
		t.Fatalf("usage frame = %+v", usage)
	}
	done := decodeOpenAIFrame(t, frameByName(t, frames, "done"))
	if !done.Done {
		t.Fatalf("done frame = %+v", done)
	}
}

func TestAnthropicStreamEventsDecodeToIR(t *testing.T) {
	frames := loadFrames(t, "anthropic_sse_frames.json")
	decode := func(name string) *ir.Chunk {
		frame := frameByName(t, frames, name)
		chunk, err := anthropic.DecodeEvent(frame.Event, frame.Data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return chunk
	}
	start := decode("message-start")
	if start.Role != "assistant" || start.HasContent || start.Usage.InputTokens != 5 {
		t.Fatalf("message_start = %+v", start)
	}
	toolStart := decode("tool-start")
	if len(toolStart.Choices) == 0 || toolStart.Choices[0].Delta.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("content_block_start = %+v", toolStart)
	}
	content := decode("content")
	if !content.HasContent || content.Choices[0].Delta.Content != "你" {
		t.Fatalf("text_delta = %+v", content)
	}
	if empty := decode("empty-content"); empty.HasContent {
		t.Fatalf("empty text_delta marked as content")
	}
	toolJSON := decode("tool-json")
	if len(toolJSON.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("input_json_delta = %+v", toolJSON)
	}
	delta := decode("message-delta")
	if delta.Usage == nil || delta.Usage.OutputTokens != 2 || delta.Choices[0].FinishReason != "stop" {
		t.Fatalf("message_delta = %+v", delta)
	}
	if stop := decode("message-stop"); !stop.Done {
		t.Fatalf("message_stop = %+v", stop)
	}
	upstreamErr := decode("error")
	if upstreamErr.Error == nil || upstreamErr.Error.UpstreamCode != "overloaded_error" {
		t.Fatalf("error event = %+v", upstreamErr)
	}
}

func TestBailianStreamFramesDecodeToIR(t *testing.T) {
	frames := loadFrames(t, "bailian_sse_frames.json")
	decode := func(name string) *ir.Chunk {
		chunk, err := bailian.DecodeChunk(framePayload(t, frameByName(t, frames, name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return chunk
	}
	if role := decode("role"); role.HasContent || role.Role != "assistant" {
		t.Fatalf("role frame = %+v", role)
	}
	if content := decode("content"); !content.HasContent {
		t.Fatalf("content frame not marked")
	}
	if usage := decode("usage"); usage.Usage == nil || usage.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage frame = %+v", usage)
	}
	if done := decode("done"); !done.Done {
		t.Fatalf("done frame = %+v", done)
	}
}

// TestOpenAIFrameEncoding covers chunk -> frame text for the three shapes a
// relay emits: content, tool call and the PRD E.4 error payload. Half-frame
// reassembly is deliberately out of scope here; it belongs to the SSE parser
// in internal/stream and is fed a complete data payload by that parser.
func TestOpenAIFrameEncoding(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		frame, err := openai.EncodeSSE(&ir.Chunk{
			ID: "c1", Model: "m", HasContent: true,
			Choices: []ir.Choice{{Delta: &ir.Delta{Content: "你好"}}},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		payload := payloadOf(t, frame)
		var wire struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if wire.Choices[0].Delta.Content != "你好" {
			t.Fatalf("content lost: %s", payload)
		}
	})

	t.Run("tool call", func(t *testing.T) {
		frame, err := openai.EncodeSSE(&ir.Chunk{
			ID: "c1", Model: "m",
			Choices: []ir.Choice{{Delta: &ir.Delta{ToolCalls: []ir.ToolCall{{
				ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`"{\"q\":1}"`),
			}}}}},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !strings.Contains(string(frame), `"name":"lookup"`) {
			t.Fatalf("tool name lost: %s", frame)
		}
	})

	t.Run("error", func(t *testing.T) {
		frame, err := openai.EncodeSSE(&ir.Chunk{Error: &ir.IRError{
			UpstreamCode: "Throttling", Message: "slow down",
		}})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if strings.Contains(string(frame), "event:") {
			t.Fatalf("OpenAI error frame must not carry an event line: %s", frame)
		}
		var wire struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payloadOf(t, frame), &wire); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if wire.Error.Code != "Throttling" || wire.Error.Message != "slow down" {
			t.Fatalf("error object = %+v", wire.Error)
		}
	})

	t.Run("done", func(t *testing.T) {
		frame, err := openai.EncodeSSE(&ir.Chunk{Done: true})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if string(frame) != "data: [DONE]\n\n" {
			t.Fatalf("done frame = %q", frame)
		}
	})
}

// TestAnthropicFrameEncoding covers chunk -> event text for the three shapes a
// relay emits: text delta, tool call (block start + input_json_delta) and the
// PRD E.4 error event.
func TestAnthropicFrameEncoding(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		frame, err := anthropic.EncodeChunkSSE(&ir.Chunk{
			ID: "msg_1", Model: "m", HasContent: true,
			Choices: []ir.Choice{{Delta: &ir.Delta{Content: "你好"}}},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !strings.HasPrefix(string(frame), "event: content_block_delta\n") {
			t.Fatalf("frame = %q", frame)
		}
		if !strings.Contains(string(frame), `"type":"text_delta"`) {
			t.Fatalf("text delta missing: %s", frame)
		}
	})

	t.Run("tool call", func(t *testing.T) {
		frame, err := anthropic.EncodeChunkSSE(&ir.Chunk{
			ID: "msg_1", Model: "m",
			Choices: []ir.Choice{{Delta: &ir.Delta{ToolCalls: []ir.ToolCall{{
				ID: "toolu_1", Name: "lookup", Arguments: json.RawMessage(`"{\"q\":1}"`),
			}}}}},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		text := string(frame)
		if !strings.Contains(text, "event: content_block_start") ||
			!strings.Contains(text, "event: content_block_delta") ||
			!strings.Contains(text, `"type":"input_json_delta"`) {
			t.Fatalf("tool frame = %s", text)
		}
	})

	t.Run("error", func(t *testing.T) {
		frame, err := anthropic.EncodeChunkSSE(&ir.Chunk{Error: &ir.IRError{
			UpstreamCode: "overloaded_error", Message: "overloaded",
		}})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !strings.HasPrefix(string(frame), "event: error\n") ||
			!strings.Contains(string(frame), `"type":"error"`) {
			t.Fatalf("error frame = %s", frame)
		}
	})

	t.Run("done", func(t *testing.T) {
		frame, err := anthropic.EncodeChunkSSE(&ir.Chunk{Done: true})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !strings.HasPrefix(string(frame), "event: message_stop\n") {
			t.Fatalf("done frame = %s", frame)
		}
	})
}

// TestAnthropicHeaders pins the header contract: x-api-key carries the
// channel secret, anthropic-version is chosen by the adapter and never by the
// client. max_tokens is mandatory in both directions.
func TestAnthropicHeaders(t *testing.T) {
	headers := anthropic.Headers("sk-upstream", "")
	if headers[anthropic.HeaderAPIKey] != "sk-upstream" {
		t.Fatalf("x-api-key = %q", headers[anthropic.HeaderAPIKey])
	}
	if headers[anthropic.HeaderVersion] != anthropic.DefaultVersion {
		t.Fatalf("anthropic-version = %q", headers[anthropic.HeaderVersion])
	}
	if got := anthropic.Headers("k", "2024-01-01")[anthropic.HeaderVersion]; got != "2024-01-01" {
		t.Fatalf("explicit version = %q", got)
	}
}

func TestAnthropicMaxTokensIsRequired(t *testing.T) {
	req := &ir.Request{Model: "claude", Messages: []ir.Message{{
		Role: "user", Content: ir.Content{Text: "hi"},
	}}}
	if _, err := anthropic.EncodeRequest(req); !errors.Is(err, anthropic.ErrMaxTokensRequired) {
		t.Fatalf("encode error = %v, want ErrMaxTokensRequired", err)
	}
	if _, err := anthropic.DecodeRequest([]byte(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)); !errors.Is(err, anthropic.ErrMaxTokensRequired) {
		t.Fatalf("decode error = %v, want ErrMaxTokensRequired", err)
	}
	max := int64(16)
	req.MaxTokens = &max
	if _, err := anthropic.EncodeRequest(req); err != nil {
		t.Fatalf("encode with max_tokens: %v", err)
	}
}

// TestAdaptersDependOnlyOnIR enforces the two hard constraints a unit test can
// see: no adapter imports internal/ or cmd/, and adapters do not import each
// other (the only allowed relaykit import is relaykit/ir).
func TestAdaptersDependOnlyOnIR(t *testing.T) {
	for _, pkg := range []string{"openai", "anthropic", "bailian"} {
		entries, err := os.ReadDir(pkg)
		if err != nil {
			t.Fatalf("read %s: %v", pkg, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(pkg, name))
			if err != nil {
				t.Fatalf("read %s/%s: %v", pkg, name, err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if !strings.Contains(line, `"yunshu.dev/`) {
					continue
				}
				if line == `"yunshu.dev/relaykit/ir"` || line == `"yunshu.dev/relaykit"` {
					continue
				}
				t.Fatalf("%s/%s imports %s; adapters may only depend on ir", pkg, name, line)
			}
		}
	}
}

// --- canonical views used by the three-hop comparison -----------------------

// vendorExtra lists members that belong to one wire dialect. They are excluded
// from the semantic comparison and asserted separately by the
// TestUnknownField* cases above.
var vendorExtra = map[string]bool{
	"logprobs": true, "top_logprobs": true, "metadata": true, "top_k": true,
	"thinking": true, "enable_search": true, "search_options": true,
	"seed": true, "user": true, "presence_penalty": true, "frequency_penalty": true,
	"system_fingerprint": true, "request_id": true, "container": true,
	"citations": true, "refusal": true, "_raw_response": true,
}

type requestView struct {
	Model       string
	Stream      bool
	System      string
	Messages    []messageView
	Tools       []toolView
	ToolChoice  any
	MaxTokens   *int64
	Temperature *float64
	TopP        *float64
	Stop        []string
	Extra       map[string]any
}

type messageView struct {
	Role       string
	Name       string
	ToolCallID string
	Text       string
	Parts      []partView
	ToolCalls  []toolCallView
	Extra      map[string]any
}

type partView struct {
	Type      string
	Text      string
	ImageURL  string
	MediaType string
	ID        string
	Name      string
	Input     any
	ToolUseID string
	Extra     map[string]any
}

type toolView struct {
	Name        string
	Description string
	Parameters  any
	Extra       map[string]any
}

type toolCallView struct {
	ID        string
	Name      string
	Arguments any
}

func canonicalRequest(req *ir.Request) requestView {
	view := requestView{
		Model:       req.Model,
		Stream:      req.Stream,
		System:      req.System,
		ToolChoice:  anyRaw(req.ToolChoice, true),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Extra:       anyExtra(req.RawExtra),
	}
	for _, tool := range req.Tools {
		view.Tools = append(view.Tools, toolView{
			Name: tool.Name, Description: tool.Description,
			Parameters: anyRaw(tool.Parameters, false), Extra: anyExtra(tool.Extra),
		})
	}
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			text := msg.Content.Text
			if text == "" {
				text = partsText(msg.Content.Parts)
			}
			if text == "" {
				continue
			}
			if view.System != "" {
				view.System += "\n"
			}
			view.System += text
			continue
		}
		view.Messages = append(view.Messages, messageView{
			Role: msg.Role, Name: msg.Name, ToolCallID: msg.ToolCallID,
			Text: msg.Content.Text, Parts: partsView(msg.Content.Parts),
			ToolCalls: callsView(msg.ToolCalls), Extra: anyExtra(msg.Extra),
		})
	}
	return view
}

func partsText(parts []ir.ContentPart) string {
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func partsView(parts []ir.ContentPart) []partView {
	var out []partView
	for _, part := range parts {
		out = append(out, partView{
			Type: part.Type, Text: part.Text, ImageURL: part.ImageURL,
			MediaType: part.MediaType, ID: part.ID, Name: part.Name,
			Input: anyRaw(part.Input, false), ToolUseID: part.ToolUseID,
			Extra: anyExtra(part.Extra),
		})
	}
	return out
}

func callsView(calls []ir.ToolCall) []toolCallView {
	var out []toolCallView
	for _, call := range calls {
		out = append(out, toolCallView{
			ID: call.ID, Name: call.Name, Arguments: anyRaw(call.Arguments, false),
		})
	}
	return out
}

type responseView struct {
	ID           string
	Model        string
	StopSequence string
	Choices      []choiceView
	Usage        *usageView
	Extra        map[string]any
}

type choiceView struct {
	Index        int
	Role         string
	Text         string
	Parts        []partView
	ToolCalls    []toolCallView
	FinishReason string
	Extra        map[string]any
}

type usageView struct {
	InputTokens      int64
	OutputTokens     int64
	TotalTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Extra            map[string]any
}

func canonicalResponse(resp *ir.Response) responseView {
	view := responseView{
		ID: resp.ID, Model: resp.Model, StopSequence: resp.StopSequence,
		Extra: anyExtra(resp.Extra),
	}
	if resp.Usage != nil {
		view.Usage = &usageView{
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:     resp.Usage.TotalTokens,
			CacheReadTokens: resp.Usage.CacheReadTokens, CacheWriteTokens: resp.Usage.CacheWriteTokens,
			Extra: anyExtra(resp.Usage.Extra),
		}
	}
	for _, choice := range resp.Choices {
		viewChoice := choiceView{
			Index: choice.Index, FinishReason: choice.FinishReason,
			Extra: anyExtra(choice.Extra),
		}
		if choice.Message != nil {
			viewChoice.Role = choice.Message.Role
			viewChoice.Text = choice.Message.Content.Text
			viewChoice.Parts = partsView(choice.Message.Content.Parts)
			viewChoice.ToolCalls = callsView(choice.Message.ToolCalls)
		}
		view.Choices = append(view.Choices, viewChoice)
	}
	return view
}

func anyExtra(extra map[string]json.RawMessage) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	out := map[string]any{}
	for name, raw := range extra {
		if vendorExtra[name] {
			continue
		}
		out[name] = anyRaw(raw, false)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func anyRaw(raw json.RawMessage, keepObject bool) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	if obj, ok := value.(map[string]any); ok && !keepObject {
		return obj
	}
	return value
}

func assertRawTrue(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil || !value {
		t.Fatalf("%s = %s (want true)", name, raw)
	}
}

func assertJSONNumber(t *testing.T, name string, raw json.RawMessage, want float64) {
	t.Helper()
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || value != want {
		t.Fatalf("%s = %s (want %v)", name, raw, want)
	}
}

func assertMetadataUser(t *testing.T, name string, raw json.RawMessage) {
	t.Helper()
	var value struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.UserID != "u-123" {
		t.Fatalf("%s metadata = %s (want user_id u-123)", name, raw)
	}
}

func payloadOf(t *testing.T, frame []byte) []byte {
	t.Helper()
	text := string(frame)
	if !strings.HasPrefix(text, "data: ") {
		t.Fatalf("frame has no data prefix: %q", text)
	}
	if !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("frame has no blank-line terminator: %q", text)
	}
	return []byte(strings.TrimSuffix(strings.TrimPrefix(text, "data: "), "\n\n"))
}

// semanticJSON converts a value to its generic JSON tree so RawMessage members
// compare by value rather than by incidental whitespace.
func semanticJSON(t *testing.T, v any) any {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func dump(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(encoded)
}
