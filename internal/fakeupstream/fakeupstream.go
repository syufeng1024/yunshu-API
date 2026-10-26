// Package fakeupstream is a development-only OpenAI-compatible upstream. It
// exists so T08's dispatch and SSE paths can be exercised end-to-end without a
// real vendor. It must never be imported by gatewayd, controld or jobsd.
package fakeupstream

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// Mode selects the failure or framing behaviour.
type Mode string

const (
	ModeOK        Mode = "ok"
	Mode429       Mode = "429"
	ModeFailFirst Mode = "500-once"
	ModeNoDone    Mode = "no-done"
	ModeHalfFrame Mode = "half-frame"
	// ModeVendorFrame injects a valid-JSON frame that is not part of the
	// OpenAI dialect. A gateway that copies bytes would forward it verbatim.
	ModeVendorFrame Mode = "vendor-frame"
	ModeSlow        Mode = "slow"
	ModeNoUsage     Mode = "no-usage"
	// ModeStreamStall emits 200 characters and then holds the stream open
	// without [DONE], so the client-abort path can be exercised.
	ModeStreamStall Mode = "stream-stall"
)

// Options configures one fake server.
type Options struct {
	Mode Mode
	// APIKey, when non-empty, must match Authorization: Bearer <key> or
	// x-api-key. It proves the gateway decrypted the channel key.
	APIKey string
	// RetryAfter is sent on a 429. Empty means no header.
	RetryAfter string
	// FailFirst makes the first N non-stream requests return 500.
	FailFirst int
	// SlowDelay delays the response headers (ModeSlow).
	SlowDelay time.Duration
	// Usage controls whether the stream ends with a usage frame.
	// ModeNoUsage forces it off.
	Usage bool
}

// Server is an http.Handler with request counters.
type Server struct {
	opts     Options
	requests atomic.Int64
}

// New builds the handler.
func New(opts Options) *Server {
	if opts.Mode == "" {
		opts.Mode = ModeOK
	}
	if opts.RetryAfter == "" {
		opts.RetryAfter = "30"
	}
	if !opts.Usage && opts.Mode != ModeNoUsage {
		opts.Usage = true
	}
	return &Server{opts: opts}
}

// Requests returns how many requests reached the handler.
func (s *Server) Requests() int64 { return s.requests.Load() }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.requests.Add(1)
	if !s.authorized(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad fake upstream key"}}`))
		return
	}
	if s.opts.Mode == ModeSlow && s.opts.SlowDelay > 0 {
		select {
		case <-time.After(s.opts.SlowDelay):
		case <-r.Context().Done():
			return
		}
	}
	if s.opts.Mode == Mode429 {
		w.Header().Set("Retry-After", s.opts.RetryAfter)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"fake upstream rate limited"}}`))
		return
	}
	if s.opts.Mode == ModeFailFirst && int(n) <= s.opts.FailFirst {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"fake upstream transient failure"}}`))
		return
	}

	var body struct {
		Model         string `json:"model"`
		Stream        bool   `json:"stream"`
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil {
		_ = jsonx.Unmarshal(raw, &body)
	}
	model := body.Model
	if model == "" {
		model = "fake-model"
	}

	if body.Stream {
		s.serveStream(w, r, model, body.StreamOptions != nil && body.StreamOptions.IncludeUsage)
		return
	}
	s.serveUnary(w, model)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.opts.APIKey == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == s.opts.APIKey {
		return true
	}
	return r.Header.Get("x-api-key") == s.opts.APIKey
}

func (s *Server) serveUnary(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-fake","object":"chat.completion","created":1700000000,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hello from fake"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`, model)
}

func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, model string, includeUsage bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	if s.opts.Mode == ModeVendorFrame {
		// Valid JSON, unknown event. A byte copier would pass this to the client;
		// a parser drops it because it carries no choices and no usage.
		_, _ = w.Write([]byte("event: vendor-debug\ndata: {\"debug\":true,\"note\":\"not-standard\"}\n\n"))
		flush()
	}

	writeFrame := func(payload string) {
		_, _ = w.Write([]byte("data: " + payload + "\n\n"))
		flush()
	}

	role := fmt.Sprintf(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`, model)
	writeFrame(role)

	if s.opts.Mode == ModeStreamStall {
		for i := 0; i < 20; i++ {
			writeFrame(fmt.Sprintf(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":%q,"choices":[{"index":0,"delta":{"content":"0123456789"},"finish_reason":null}]}`, model))
		}
		<-r.Context().Done()
		return
	}

	if s.opts.Mode == ModeHalfFrame {
		// The frame is split inside the JSON string. Only a buffering parser
		// reassembles it.
		first := fmt.Sprintf(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":%q,"choices":[{"index":0,"delta":{"content":"hel`, model)
		second := `lo half"}}]}`
		_, _ = w.Write([]byte("data: " + first))
		flush()
		select {
		case <-time.After(10 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(second + "\n\n"))
		flush()
	} else {
		writeFrame(fmt.Sprintf(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":%q,"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`, model))
	}
	writeFrame(fmt.Sprintf(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":%q,"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`, model))
	writeFrame(fmt.Sprintf(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, model))

	if s.opts.Usage && s.opts.Mode != ModeNoUsage && includeUsage {
		writeFrame(`{"id":"chatcmpl-fake","object":"chat.completion.chunk","created":1700000000,"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`)
	}
	if s.opts.Mode == ModeNoDone {
		return
	}
	writeFrame("[DONE]")
}
