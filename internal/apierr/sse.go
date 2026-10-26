package apierr

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"yunshu.dev/core/internal/jsonx"
)

// SSEProtocol selects the frame carrier. The error object is identical; only
// the presence of the `event: error` line differs (PRD E.4 rule 7):
//
//	Anthropic: event: error\ndata: {"error":{...}}\n\n
//	OpenAI:    data: {"error":{...}}\n\n
type SSEProtocol string

const (
	SSEAnthropic SSEProtocol = "anthropic"
	SSEOpenAI    SSEProtocol = "openai"
)

// ErrUnknownSSEProtocol is returned instead of guessing a carrier.
var ErrUnknownSSEProtocol = errors.New("apierr: unknown SSE protocol")

// WriteSSE emits one terminal error frame. It never appends [DONE] and never
// emits a settlement frame: a strict SDK treats both as corruption or as a
// normal end.
//
// credits_consumed is the only stream-side disclosure of the actual charge
// (E.4 rule 8). It defaults to 0 when the caller did not set it, and partial
// is always true for a post-first-byte failure.
func WriteSSE(w io.Writer, e *APIError, proto SSEProtocol) error {
	e = normalize(e)
	if proto != SSEAnthropic && proto != SSEOpenAI {
		return ErrUnknownSSEProtocol
	}
	e.EnsureRequestID()

	frame := *e
	frame.sse = true
	frame.Partial = true
	if frame.CreditsConsumed == nil {
		zero := int64(0)
		frame.CreditsConsumed = &zero
	}

	payload, err := jsonx.Marshal(&frame)
	if err != nil {
		return err
	}
	if proto == SSEAnthropic {
		if _, err := io.WriteString(w, "event: error\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

// WriteSSEFlush writes the frame and flushes, so the client sees the failure
// before the TCP connection is closed.
func WriteSSEFlush(w http.ResponseWriter, e *APIError, proto SSEProtocol) error {
	if err := WriteSSE(w, e, proto); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
