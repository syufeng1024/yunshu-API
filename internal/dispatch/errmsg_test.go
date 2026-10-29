package dispatch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"yunshu.dev/core/internal/apierr"
)

// E-09's templates for upstream_timeout and upstream_all_failed both contain
// "%d 次". A caller that builds them with New instead of Newf ships the literal
// placeholder to the user. This was found on 2026-10-29 during a T39
// counter-proof: removing the TLS branch made the body read
// "上游服务当前不可用，已重试 %d 次".
func TestClassifyTransportErrorFormatsAttemptCount(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		class, api := classifyTransportError(timeoutErr{}, 3)
		if class != ClassTimeout || api.Code != apierr.CodeUpstreamTimeout {
			t.Fatalf("class/code = %s/%s", class, api.Code)
		}
		assertInterpolated(t, api.Message, "已重试 3 次")
	})

	t.Run("connection refused", func(t *testing.T) {
		class, api := classifyTransportError(errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), 2)
		if class != ClassConnectRefused || api.Code != apierr.CodeUpstreamAllFailed {
			t.Fatalf("class/code = %s/%s", class, api.Code)
		}
		assertInterpolated(t, api.Message, "已重试 2 次")
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		_, api := classifyTransportError(context.DeadlineExceeded, 1)
		assertInterpolated(t, api.Message, "已重试 1 次")
	})
}

// assertInterpolated fails when a verb survived formatting or the expected
// count is missing.
func assertInterpolated(t *testing.T, message, want string) {
	t.Helper()
	if strings.Contains(message, "%!") || strings.Contains(message, "%d") {
		t.Fatalf("message still carries an unformatted verb: %q", message)
	}
	if !strings.Contains(message, want) {
		t.Fatalf("message = %q, want it to contain %q", message, want)
	}
}

// TestConnectionRefusedBodyIsUserReadable proves the whole path, not just the
// classifier: a closed loopback port must yield a 502 whose body has real
// numbers and no placeholder.
func TestConnectionRefusedBodyIsUserReadable(t *testing.T) {
	_, envelope := testBox(t)
	cat := testCatalog(t, "http://127.0.0.1:1/v1", envelope, nil)
	d := newDispatcher(t, cat, &fakeLedger{}, &attemptRecorder{}, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "%d") || strings.Contains(body, "%!") {
		t.Fatalf("body carries an unformatted verb: %s", body)
	}
	if !strings.Contains(body, "已重试 1 次") {
		t.Fatalf("body = %s, want 已重试 1 次", body)
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeUpstreamAllFailed {
		t.Fatalf("code = %v, want %s", got, apierr.CodeUpstreamAllFailed)
	}
}

// timeoutErr is a net.Error whose Timeout() is true.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}
