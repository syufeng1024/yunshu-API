package stream

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/relaykit/ir"
)

type counter struct{}

func (counter) CountInput(text string) (int64, error) { return int64(len(text)), nil }

func relayBody(t *testing.T, input string, opts Options) (Result, *httptest.ResponseRecorder, error) {
	t.Helper()
	if opts.W == nil {
		opts.W = httptest.NewRecorder()
	}
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now()
	}
	if opts.Tokens == nil {
		opts.Tokens = counter{}
	}
	rec := opts.W.(*httptest.ResponseRecorder)
	res, err := Relay(context.Background(), io.NopCloser(strings.NewReader(input)), opts)
	return res, rec, err
}

func frame(content string) string {
	return `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"` +
		content + `"},"finish_reason":null}]}` + "\n\n"
}

// A8: upstream EOF without [DONE]. The emitted content is kept, usage falls
// back to local estimation, and the dispatcher is asked for an error frame.
func TestA8NoDoneIsPartialWithEstimatedUsage(t *testing.T) {
	input := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
		frame("hello") + frame(" world")
	res, rec, err := relayBody(t, input, Options{
		ClientProtocol:   ProtoOpenAI,
		UpstreamProtocol: ProtoOpenAI,
		PromptTokens:     5,
		PreheldMicro:     1000,
		RemainingCredits: 9000,
		FirstByteTimeout: time.Second,
		IdleTimeout:      time.Second,
	})
	if err == nil || !res.Started {
		t.Fatalf("relay: %+v %v; a truncated stream must start and report the truncation", res, err)
	}
	if !res.UpstreamTruncated || !res.NeedErrorFrame {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage.Source != SourceEstimate || res.Usage.TokensOut == 0 {
		t.Fatalf("usage = %+v; a no-usage stream must not settle at zero", res.Usage)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, " world") {
		t.Fatalf("content missing: %s", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("a truncated stream must not append [DONE]: %s", body)
	}
}

// A9: a role-only frame precedes the first content frame; first_token_ms is
// taken at the content frame, not at the role frame.
func TestA9FirstTokenIgnoresRoleFrame(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n"))
		time.Sleep(60 * time.Millisecond)
		_, _ = pw.Write([]byte(frame("hi")))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
		_ = pw.Close()
	}()
	started := time.Now()
	rec := httptest.NewRecorder()
	res, err := Relay(context.Background(), pr, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		W: rec, StartedAt: started, PromptTokens: 2,
		FirstByteTimeout: time.Second, IdleTimeout: time.Second,
		Tokens: counter{},
	})
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if res.FirstTokenMS == nil || *res.FirstTokenMS < 50 {
		t.Fatalf("first_token_ms = %v, want >= 50 (role frame must not count)", res.FirstTokenMS)
	}
}

// A11: silence longer than the interval produces exactly one comment frame;
// a steady stream does not.
func TestA11HeartbeatOnlyDuringSilence(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(frame("a")))
		time.Sleep(100 * time.Millisecond)
		_, _ = pw.Write([]byte(frame("b")))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
		_ = pw.Close()
	}()
	rec := httptest.NewRecorder()
	_, err := Relay(context.Background(), pr, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		W: rec, StartedAt: time.Now(), PromptTokens: 1,
		FirstByteTimeout: time.Second, IdleTimeout: time.Second,
		HeartbeatInterval: 30 * time.Millisecond,
		Tokens:            counter{},
	})
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if !strings.Contains(rec.Body.String(), ": keep-alive") {
		t.Fatalf("heartbeat missing: %s", rec.Body.String())
	}

	pr2, pw2 := io.Pipe()
	go func() {
		for i := 0; i < 8; i++ {
			_, _ = pw2.Write([]byte(frame("x")))
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = pw2.Write([]byte("data: [DONE]\n\n"))
		_ = pw2.Close()
	}()
	rec2 := httptest.NewRecorder()
	_, err = Relay(context.Background(), pr2, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		W: rec2, StartedAt: time.Now(), PromptTokens: 1,
		FirstByteTimeout: time.Second, IdleTimeout: time.Second,
		HeartbeatInterval: 200 * time.Millisecond,
		Tokens:            counter{},
	})
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if strings.Contains(rec2.Body.String(), ": keep-alive") {
		t.Fatalf("heartbeat injected while data was flowing: %s", rec2.Body.String())
	}
}

// A12: the stream header set never contains X-Credits-Consumed.
func TestStreamHeadersHaveNoConsumed(t *testing.T) {
	res, rec, err := relayBody(t, frame("hi")+"data: [DONE]\n\n", Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		RequestID: "rid-stream", PromptTokens: 1, PreheldMicro: 700, RemainingCredits: 9300,
		FirstByteTimeout: time.Second, IdleTimeout: time.Second,
	})
	if err != nil || !res.Completed {
		t.Fatalf("relay: %+v %v", res, err)
	}
	h := rec.Header()
	if h.Get("X-Credits-Consumed") != "" {
		t.Fatalf("stream must not send X-Credits-Consumed: %v", h)
	}
	if h.Get("X-Credits-Preheld") != "700" || h.Get("X-Credits-Remaining") != "9300" {
		t.Fatalf("credit headers = %v", h)
	}
	if !strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q", h.Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("normal stream must end with [DONE]: %s", rec.Body.String())
	}
}

// A13 support: a vendor frame is dropped by the parser, never re-emitted.
func TestVendorFrameIsNotForwarded(t *testing.T) {
	input := "event: vendor-debug\ndata: {\"debug\":true}\n\n" + frame("hi") + "data: [DONE]\n\n"
	res, rec, err := relayBody(t, input, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		PromptTokens: 1, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
	})
	if err != nil || !res.Completed {
		t.Fatalf("relay: %+v %v", res, err)
	}
	if strings.Contains(rec.Body.String(), "vendor-debug") || strings.Contains(rec.Body.String(), "debug") {
		t.Fatalf("vendor frame leaked verbatim: %s", rec.Body.String())
	}
}

// usage frames after [DONE] are still adopted (TDS E-33).
func TestUsageAfterDoneIsAdopted(t *testing.T) {
	input := frame("hi") + "data: [DONE]\n\n" +
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}` + "\n\n"
	res, _, err := relayBody(t, input, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		PromptTokens: 1, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
	})
	if err != nil || !res.Completed {
		t.Fatalf("relay: %+v %v", res, err)
	}
	if res.Usage.Source != SourceUpstream || res.Usage.TokensIn != 9 || res.Usage.TokensOut != 4 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

// The resolver must never treat an empty upstream usage block as a free call.
func TestResolveUsageFallsBackWhenUpstreamIsZero(t *testing.T) {
	u := ResolveUsage(&ir.Usage{}, 5, "some output", counter{})
	if u.TokensOut == 0 || u.Source != SourceEstimate {
		t.Fatalf("usage = %+v", u)
	}
}

func TestFirstByteTimeoutBeforeStart(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
	}()
	res, err := Relay(ctx, pr, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		W: httptest.NewRecorder(), PromptTokens: 1,
		FirstByteTimeout: 30 * time.Millisecond, IdleTimeout: time.Second,
		Tokens: counter{},
	})
	if !errors.Is(err, ErrBeforeFirstByte) || res.Started {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestIdleTimeoutAfterStartNeedsErrorFrame(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(frame("partial")))
		// no more data, no close: the idle timer must fire
	}()
	defer pw.Close()
	res, err := Relay(context.Background(), pr, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		W: httptest.NewRecorder(), PromptTokens: 1,
		FirstByteTimeout: time.Second, IdleTimeout: 40 * time.Millisecond,
		Tokens: counter{},
	})
	if !errors.Is(err, ErrUpstreamIdle) || !res.Started || !res.NeedErrorFrame {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if res.Usage.Source != SourceEstimate || res.Usage.TokensOut != int64(len("partial")) {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestMalformedFrameIsForwardedRaw(t *testing.T) {
	input := frame("a") + "data: {not json}\n\n" + frame("b") + "data: [DONE]\n\n"
	res, rec, err := relayBody(t, input, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoOpenAI,
		PromptTokens: 1, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
	})
	if err != nil || !res.Completed {
		t.Fatalf("relay: %+v %v", res, err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "{not json}") {
		t.Fatalf("malformed frame was silently dropped: %s", body)
	}
	if !strings.Contains(body, `"content":"a"`) || !strings.Contains(body, `"content":"b"`) {
		t.Fatalf("valid frames around the malformed one were lost: %s", body)
	}
}

func TestAnthropicClientGetsEventFrames(t *testing.T) {
	res, rec, err := relayBody(t, frame("hi")+"data: [DONE]\n\n", Options{
		ClientProtocol: ProtoAnthropic, UpstreamProtocol: ProtoOpenAI,
		PromptTokens: 3, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
	})
	if err != nil || !res.Completed {
		t.Fatalf("relay: %+v %v", res, err)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("Anthropic client must not receive [DONE]: %s", body)
	}
}

func TestAnthropicUpstreamUsageIsMerged(t *testing.T) {
	input := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	res, rec, err := relayBody(t, input, Options{
		ClientProtocol: ProtoOpenAI, UpstreamProtocol: ProtoAnthropic,
		PromptTokens: 1, FirstByteTimeout: time.Second, IdleTimeout: time.Second,
	})
	if err != nil || !res.Completed {
		t.Fatalf("relay: %+v %v", res, err)
	}
	if res.Usage.Source != SourceUpstream || res.Usage.TokensIn != 5 || res.Usage.TokensOut != 2 {
		t.Fatalf("usage = %+v (input must not be lost when output arrives later)", res.Usage)
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
