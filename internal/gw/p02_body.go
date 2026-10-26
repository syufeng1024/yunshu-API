package gw

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"yunshu.dev/core/internal/apierr"
)

// MaxBodyBytes is the P02 request-body ceiling (TDS §3.1). V1.0 has no
// configuration key for it — the 45-key registry does not contain
// max_body_bytes — so the value is a named constant rather than a snapshot
// read. Adding a key would require a migration and is deliberately out of
// scope for T07.
const MaxBodyBytes = 10 << 20

// BodyReadTimeout is the P02 read budget (TDS §3.1). Like the body ceiling it
// has no configuration key; it is a fixed stage budget, not a policy knob.
const BodyReadTimeout = 5 * time.Second

// ReadBody reads the request body under both limits.
//
// A Content-Length above the ceiling is rejected before reading a byte, so an
// 11 MB upload costs one header parse rather than 10 MB of transfer.
func ReadBody(w http.ResponseWriter, r *http.Request, maxBytes int64, timeout time.Duration) ([]byte, *apierr.APIError) {
	if maxBytes <= 0 {
		maxBytes = MaxBodyBytes
	}
	if timeout <= 0 {
		timeout = BodyReadTimeout
	}
	if r.ContentLength > maxBytes {
		return nil, apierr.New(apierr.CodeRequestTooLarge)
	}

	// A deadline is set on the connection, not on the request context: the
	// request context also covers the upstream call, which must outlive P02 by
	// two orders of magnitude. CommandContext-style budgets are per stage.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()

	limited := http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, apierr.New(apierr.CodeRequestTooLarge)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, apierr.New(apierr.CodeRequestReadTimeout)
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, apierr.New(apierr.CodeInvalidRequestBody).WithCause(err)
		}
		return nil, apierr.Newf(apierr.CodeInvalidRequestBody, "读取请求体失败").WithCause(err)
	}
	return body, nil
}
