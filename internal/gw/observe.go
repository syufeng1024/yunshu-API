package gw

import (
	"net/http"
	"time"
)

// statusRecorder captures the status code for gw_requests_total without
// changing the response. Unwrap keeps http.NewResponseController able to reach
// the connection (P01 connection reset and the streaming flusher both use it).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusRecorder) Status() int {
	if !w.wroteHeader {
		return http.StatusOK
	}
	return w.status
}

// observeRequest records the request-level pair. It runs after the response is
// written; every metric method is non-blocking and nil-safe.
func (h *Handler) observeRequest(c *Ctx, status int) {
	if h == nil || h.d == nil || h.d.Metrics == nil || c == nil {
		return
	}
	if c.ErrCode == "connection_reset" {
		// P01 hijacked the connection and wrote no HTTP status.
		status = 0
	}
	model := modelLabel(c)
	h.d.Metrics.Request(model, protocolLabel(c.Protocol), outboundProtocolLabel(c), c.ErrCode, status)
	h.d.Metrics.RequestDuration(model, c.Stream, time.Since(c.StartedAt))
}

func modelLabel(c *Ctx) string {
	if c != nil && c.Model != nil && c.Model.ModelName != "" {
		return c.Model.ModelName
	}
	return "unknown"
}

func protocolLabel(p Protocol) string {
	switch string(p) {
	case string(ProtocolOpenAI), string(ProtocolAnthropic):
		return string(p)
	default:
		return "none"
	}
}

// outboundProtocolLabel reads the chosen route's protocol. It never falls back
// to the requested model name or any other user input.
func outboundProtocolLabel(c *Ctx) string {
	if c == nil || c.Route == nil {
		return "none"
	}
	switch c.Route.ProtocolID {
	case 1:
		return "BAILIAN"
	case 2:
		return "OPENAI_COMPAT"
	case 3:
		return "ANTHROPIC_COMPAT"
	default:
		return "none"
	}
}
