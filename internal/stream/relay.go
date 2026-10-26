package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/billing"
	"yunshu.dev/relaykit/anthropic"
	"yunshu.dev/relaykit/ir"
	"yunshu.dev/relaykit/openai"
)

// Protocol names shared with the route's protocol row.
const (
	ProtoOpenAI    = "OPENAI_COMPAT"
	ProtoAnthropic = "ANTHROPIC_COMPAT"
	ProtoBailian   = "BAILIAN"
)

// Relay errors. BeforeStarted errors have flushed no bytes; the dispatcher may
// retry them when the retry predicate allows it.
var (
	ErrBeforeFirstByte = errors.New("stream: first byte not received before deadline")
	ErrClientAbort     = errors.New("stream: client aborted")
	ErrUpstreamEmpty   = errors.New("stream: upstream ended without a frame")
	ErrUpstreamIdle    = errors.New("stream: upstream idle timeout")
	ErrMalformedFrame  = errors.New("stream: malformed upstream frame")
	ErrSlowClient      = errors.New("stream: client write timed out three times")
)

// Options is one stream relay.
type Options struct {
	ClientProtocol   string
	UpstreamProtocol string

	W         http.ResponseWriter
	RequestID string

	PromptTokens     int64
	StartedAt        time.Time
	PreheldMicro     int64
	RemainingCredits float64

	FirstByteTimeout  time.Duration
	IdleTimeout       time.Duration
	HeartbeatInterval time.Duration
	WriteTimeout      time.Duration

	Tokens billing.InputCounter
}

// Result is the auditable outcome of one stream, used for partial settlement.
type Result struct {
	Started   bool
	Completed bool

	ClientAborted     bool
	UpstreamTruncated bool
	// NeedErrorFrame asks the dispatcher to emit event:error after settlement.
	NeedErrorFrame bool

	FlushedBytes int64
	FirstTokenMS *int64

	Usage      Usage
	StopReason string

	UpstreamID    string
	UpstreamModel string

	LastError error
}

type frameResult struct {
	frame Frame
	err   error
}

// Relay parses, converts and re-emits one upstream SSE stream.
//
// The function never copies upstream bytes wholesale. The only raw pass-through
// is a single malformed frame, which TDS §4.1 step 4 requires forwarding so a
// vendor diagnostic is not silently swallowed.
func Relay(ctx context.Context, body io.ReadCloser, opts Options) (Result, error) {
	if body == nil {
		return Result{}, ErrUpstreamEmpty
	}
	defer body.Close()

	if opts.FirstByteTimeout <= 0 {
		opts.FirstByteTimeout = 30 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 5 * time.Second
	}
	if opts.ClientProtocol == "" {
		opts.ClientProtocol = ProtoOpenAI
	}
	if opts.UpstreamProtocol == "" {
		opts.UpstreamProtocol = ProtoOpenAI
	}

	parser := NewParser(body)
	frames := make(chan frameResult, 1)
	done := make(chan struct{})
	defer close(done)
	go pump(parser, frames, done)

	// Pause cancellation before starting: the request context carries both the
	// client disconnection and the total budget.
	firstChunk, firstRaw, err := waitFirst(ctx, frames, opts)
	if err != nil {
		return Result{ClientAborted: errors.Is(err, ErrClientAbort)}, err
	}

	outWriter := &relayWriter{w: opts.W, timeout: opts.WriteTimeout}
	if err := outWriter.writeHead(opts); err != nil {
		return Result{}, err
	}
	result := Result{Started: true}
	out := &accumulatedOutput{}
	var upstreamUsage *ir.Usage
	var enc anthropic.StreamEncoder
	var firstTokenSet bool
	stopReason := ""

	emit := func(chunk *ir.Chunk) error {
		if chunk.ID != "" {
			result.UpstreamID = chunk.ID
		}
		if chunk.Model != "" {
			result.UpstreamModel = chunk.Model
		}
		if chunk.ID == "" {
			chunk.ID = result.UpstreamID
		}
		if chunk.Model == "" {
			chunk.Model = result.UpstreamModel
		}
		for _, ch := range chunk.Choices {
			if ch.Delta == nil {
				continue
			}
			out.addText(ch.Delta.Content)
			for _, tc := range ch.Delta.ToolCalls {
				out.addToolArguments(string(tc.Arguments))
			}
			if ch.Delta.Content != "" && !firstTokenSet {
				firstTokenSet = true
				result.FirstTokenMS = FirstTokenMS(opts.StartedAt, time.Now())
			}
			if ch.FinishReason != "" {
				stopReason = ch.FinishReason
			}
		}
		mergeUsage(&upstreamUsage, chunk.Usage)
		return outWriter.emit(opts.ClientProtocol, &enc, chunk, opts.PromptTokens)
	}

	if err := emit(firstChunk); err != nil {
		result.LastError = err
		return result, err
	}
	result.FlushedBytes = outWriter.bytes
	_ = firstRaw

	idle := time.NewTimer(opts.IdleTimeout)
	heartbeat := time.NewTimer(opts.HeartbeatInterval)
	defer idle.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			result.MarkClientAbort(result.Completed)
			result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
			if result.Completed {
				return result, nil
			}
			return result, ErrClientAbort

		case <-idle.C:
			result.UpstreamTruncated = !result.Completed
			result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
			if result.Completed {
				return result, nil
			}
			result.NeedErrorFrame = true
			result.LastError = ErrUpstreamIdle
			return result, ErrUpstreamIdle

		case <-heartbeat.C:
			if result.Completed {
				heartbeat.Reset(opts.HeartbeatInterval)
				continue
			}
			if err := outWriter.heartbeat(); err != nil {
				result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
				if errors.Is(err, ErrSlowClient) {
					result.UpstreamTruncated = true
					result.NeedErrorFrame = false
				}
				result.LastError = err
				return result, err
			}
			heartbeat.Reset(opts.HeartbeatInterval)

		case res := <-frames:
			if result.Completed {
				// E-33: keep the socket for a 2s trailing-usage window, never
				// the full 30s frame-idle timeout.
				resetTimer(idle, 2*time.Second)
			} else {
				resetTimer(idle, opts.IdleTimeout)
			}
			resetTimer(heartbeat, opts.HeartbeatInterval)

			if res.err != nil {
				if errors.Is(res.err, io.EOF) {
					result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
					if result.Completed {
						return result, nil
					}
					result.UpstreamTruncated = true
					result.NeedErrorFrame = true
					result.LastError = ErrUpstreamEmpty
					return result, ErrUpstreamEmpty
				}
				result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
				if result.Completed {
					return result, nil
				}
				result.UpstreamTruncated = true
				result.NeedErrorFrame = true
				result.LastError = res.err
				return result, res.err
			}

			frame := res.frame
			if frame.Comment || len(frame.Data) == 0 {
				continue
			}
			chunk, decodeErr := decodeFrame(opts.UpstreamProtocol, frame)
			if decodeErr != nil {
				if errors.Is(decodeErr, ErrUpstreamEvent) {
					result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
					result.NeedErrorFrame = true
					result.LastError = decodeErr
					return result, decodeErr
				}
				// A malformed frame is forwarded raw (TDS §4.1 step 4) and does
				// not terminate the stream.
				if opts.ClientProtocol == ProtoOpenAI {
					if err := outWriter.raw(frame); err != nil {
						result.LastError = err
						return result, err
					}
				}
				continue
			}
			if chunk.Done {
				if !result.Completed {
					if err := outWriter.finish(opts.ClientProtocol, &enc, stopReason,
						ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)); err != nil {
						result.LastError = err
						return result, err
					}
					result.Completed = true
					resetTimer(idle, 2*time.Second)
				}
				continue
			}
			if result.Completed {
				// Frames after [DONE] are read for usage only (TDS E-33); the
				// client must not receive a second terminator or settlement frame.
				mergeUsage(&upstreamUsage, chunk.Usage)
				continue
			}
			if !forwardable(chunk) {
				continue
			}
			if err := emit(chunk); err != nil {
				result.Usage = ResolveUsage(upstreamUsage, opts.PromptTokens, out.String(), opts.Tokens)
				result.LastError = err
				if errors.Is(err, ErrSlowClient) {
					result.UpstreamTruncated = true
					result.NeedErrorFrame = false
					return result, err
				}
				return result, err
			}
			result.FlushedBytes = outWriter.bytes
		}
	}
}

// waitFirst pulls frames until one can be re-emitted. Nothing is written
// before it returns, which keeps the retry window open (flushedBytes == 0).
func waitFirst(ctx context.Context, frames <-chan frameResult, opts Options) (*ir.Chunk, *Frame, error) {
	timer := time.NewTimer(opts.FirstByteTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ErrClientAbort
		case <-timer.C:
			return nil, nil, ErrBeforeFirstByte
		case res := <-frames:
			if res.err != nil {
				if errors.Is(res.err, io.EOF) {
					return nil, nil, ErrUpstreamEmpty
				}
				return nil, nil, res.err
			}
			frame := res.frame
			if frame.Comment || len(frame.Data) == 0 {
				// Comments and vendor-only frames do not satisfy the first-byte
				// deadline; the timer is absolute from request start.
				continue
			}
			if frame.Truncated {
				return nil, nil, ErrUpstreamEmpty
			}
			chunk, err := decodeFrame(opts.UpstreamProtocol, frame)
			if err != nil {
				if errors.Is(err, ErrUpstreamEvent) {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("%w: %v", ErrMalformedFrame, err)
			}
			if chunk.Done {
				return nil, nil, ErrUpstreamEmpty
			}
			if !forwardable(chunk) {
				continue
			}
			return chunk, &frame, nil
		}
	}
}

func pump(parser *Parser, out chan<- frameResult, done <-chan struct{}) {
	for {
		frame, err := parser.Next()
		select {
		case out <- frameResult{frame: frame, err: err}:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// mergeUsage combines protocol-split usage. Anthropic reports input tokens in
// message_start and output tokens in message_delta; replacing the struct would
// silently drop the input half and underbill the request.
func mergeUsage(dst **ir.Usage, src *ir.Usage) {
	if src == nil {
		return
	}
	if *dst == nil {
		*dst = &ir.Usage{}
	}
	if src.InputTokens > 0 {
		(*dst).InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		(*dst).OutputTokens = src.OutputTokens
	}
	if src.CacheHitTokens > 0 {
		(*dst).CacheHitTokens = src.CacheHitTokens
	}
	if src.CacheWriteTokens > 0 {
		(*dst).CacheWriteTokens = src.CacheWriteTokens
	}
	(*dst).TotalTokens = (*dst).InputTokens + (*dst).OutputTokens
}

func forwardable(chunk *ir.Chunk) bool {
	if chunk == nil {
		return false
	}
	return len(chunk.Choices) > 0 || chunk.Usage != nil
}

// ErrUpstreamEvent marks an explicit upstream error event (Anthropic
// "event: error"). It is not forwarded as a normal frame.
var ErrUpstreamEvent = errors.New("stream: upstream error event")

func decodeFrame(proto string, frame Frame) (*ir.Chunk, error) {
	switch proto {
	case ProtoAnthropic:
		if frame.Event == "error" || bytes.Contains(frame.Data, []byte(`"type":"error"`)) {
			return nil, fmt.Errorf("%w: %s", ErrUpstreamEvent, truncate(string(frame.Data), 512))
		}
		return anthropic.DecodeEvent(frame.Event, frame.Data)
	default:
		chunk, err := openai.DecodeChunk(frame.Data)
		if err != nil {
			return nil, err
		}
		if chunk != nil {
			if _, hasError := chunk.Extra["error"]; hasError {
				return nil, fmt.Errorf("%w: %s", ErrUpstreamEvent, truncate(string(frame.Data), 512))
			}
		}
		return chunk, nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type relayWriter struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	timeout  time.Duration
	timeouts int
	bytes    int64
	wrote    bool
}

func (w *relayWriter) writeHead(opts Options) error {
	preheld := opts.PreheldMicro
	remaining := opts.RemainingCredits
	// SetResponseHeaders knows the stream contract: it never writes
	// X-Credits-Consumed and adds Cache-Control + X-Accel-Buffering.
	apierr.SetResponseHeaders(w.w, apierr.ResponseMeta{
		RequestID:        opts.RequestID,
		Stream:           true,
		CreditsPreheld:   &preheld,
		CreditsRemaining: &remaining,
	})
	h := w.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Connection", "keep-alive")
	w.rc = http.NewResponseController(w.w)
	_ = w.rc.SetWriteDeadline(time.Now().Add(w.timeout))
	w.w.WriteHeader(http.StatusOK)
	w.wrote = true
	return w.flush()
}

func (w *relayWriter) emit(clientProto string, enc *anthropic.StreamEncoder, chunk *ir.Chunk, promptTokens int64) error {
	if clientProto == ProtoAnthropic {
		var events []anthropic.Event
		if !enc.Started() {
			events = append(events, enc.Start(chunk.ID, chunk.Model, promptTokens)...)
		}
		for _, c := range chunk.Choices {
			if c.Delta != nil && c.Delta.Content != "" {
				events = append(events, enc.Content(c.Delta.Content)...)
			}
		}
		for _, ev := range events {
			if err := w.named(ev.Name, ev.Data); err != nil {
				return err
			}
		}
		return nil
	}
	payload, err := openai.EncodeChunk(chunk)
	if err != nil {
		return err
	}
	return w.data(payload)
}

func (w *relayWriter) finish(clientProto string, enc *anthropic.StreamEncoder, stopReason string, usage Usage) error {
	if clientProto == ProtoAnthropic {
		if !enc.Started() {
			if err := w.namedEvents(enc.Start("", "", usage.TokensIn)); err != nil {
				return err
			}
		}
		return w.namedEvents(enc.Finish(stopReason, usage.TokensOut))
	}
	return w.data(openai.DonePayload)
}

func (w *relayWriter) namedEvents(events []anthropic.Event) error {
	for _, ev := range events {
		if err := w.named(ev.Name, ev.Data); err != nil {
			return err
		}
	}
	return nil
}

func (w *relayWriter) data(payload []byte) error {
	return w.rawBytes([]byte("data: " + string(payload) + "\n\n"))
}

func (w *relayWriter) named(name string, payload []byte) error {
	return w.rawBytes([]byte("event: " + name + "\ndata: " + string(payload) + "\n\n"))
}

func (w *relayWriter) raw(frame Frame) error {
	raw := framesRaw(frame)
	return w.rawBytes(raw)
}

func (w *relayWriter) heartbeat() error {
	return w.rawBytes(KeepAliveFrame())
}

func (w *relayWriter) rawBytes(p []byte) error {
	if w.rc != nil {
		_ = w.rc.SetWriteDeadline(time.Now().Add(w.timeout))
	}
	n, err := w.w.Write(p)
	w.bytes += int64(n)
	if err != nil {
		if isTimeout(err) {
			w.timeouts++
			if w.timeouts >= 3 {
				return ErrSlowClient
			}
		}
		return err
	}
	if err := w.flush(); err != nil {
		if isTimeout(err) {
			w.timeouts++
			if w.timeouts >= 3 {
				return ErrSlowClient
			}
		}
		return err
	}
	return nil
}

func (w *relayWriter) flush() error {
	if w.rc != nil {
		if err := w.rc.Flush(); err == nil || errors.Is(err, http.ErrNotSupported) {
			return nil
		} else {
			return err
		}
	}
	if f, ok := w.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func framesRaw(frame Frame) []byte {
	if len(frame.Raw) > 0 {
		if strings.HasSuffix(string(frame.Raw), "\n\n") {
			return frame.Raw
		}
		return append(append([]byte(nil), frame.Raw...), '\n', '\n')
	}
	return []byte("data: " + string(frame.Data) + "\n\n")
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
