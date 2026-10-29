package dispatch

// T39 segment 2 (OI-07): one automated trigger per error code that had a
// producer but no test. These cases assert the code value, not just the HTTP
// status, and each one turns red when its production branch is removed.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/fakeupstream"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/idemstore"
)

// upstream_tls_failure (dispatch.go:759): the dispatcher's per-channel
// transport deliberately does not trust the httptest CA, so the handshake
// fails with an x509 error whose text contains "tls:".
func TestUpstreamTLSFailureIs502WithoutRetry(t *testing.T) {
	up := httptest.NewTLSServer(fakeupstream.New(fakeupstream.Options{}))
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeUpstreamTLSFailure {
		t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeUpstreamTLSFailure, rec.Body.String())
	}

	attempts := recs.all()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 (a TLS failure is not retried)", len(attempts))
	}
	a := attempts[0]
	if a.ErrorClass != ClassTLS {
		t.Fatalf("error_class = %q, want %q", a.ErrorClass, ClassTLS)
	}
	if a.RetryDecision != DecisionNoRetry || a.IsRetryable {
		t.Fatalf("retry = %q/%v, want NO_RETRY/false", a.RetryDecision, a.IsRetryable)
	}
	if led.settleCount() != 1 || led.lastSettle().ConsumedMicro != 0 {
		t.Fatalf("a TLS failure must release the whole hold: %+v", led.settles)
	}
}

// upstream_auth_failed (dispatch.go:784): an upstream 401 and an upstream 403
// both land on the same code; the table proves the mapping is not a single
// hard-coded status.
func TestUpstreamAuthFailedMaps401And403To502(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("upstream_%d", status), func(t *testing.T) {
			up := fixedUpstream(status, `{"error":{"message":"upstream rejected the key"}}`)
			defer up.Close()
			_, envelope := testBox(t)
			cat := testCatalog(t, up.URL+"/v1", envelope, nil)
			recs := &attemptRecorder{}
			d := newDispatcher(t, cat, &fakeLedger{}, recs, &fakeIdem{})

			rec := serve(t, d, testCtx(false))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
			}
			if got := errorCode(t, rec)["code"]; got != apierr.CodeUpstreamAuthFailed {
				t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeUpstreamAuthFailed, rec.Body.String())
			}
			attempts := recs.all()
			if len(attempts) != 1 {
				t.Fatalf("attempts = %d, want 1", len(attempts))
			}
			a := attempts[0]
			if a.HTTPStatus != status || a.ErrorClass != ClassHTTP4XX {
				t.Fatalf("attempt = %d/%s, want %d/%s", a.HTTPStatus, a.ErrorClass, status, ClassHTTP4XX)
			}
			if a.RetryDecision != DecisionNoRetry || a.IsRetryable {
				t.Fatalf("retry = %q/%v, want NO_RETRY/false", a.RetryDecision, a.IsRetryable)
			}
		})
	}
}

// upstream_model_not_found (dispatch.go:786).
func TestUpstreamModelNotFoundMaps404To502(t *testing.T) {
	up := fixedUpstream(http.StatusNotFound, `{"error":{"message":"model not found at upstream"}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, &fakeLedger{}, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeUpstreamModelNotFound {
		t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeUpstreamModelNotFound, rec.Body.String())
	}
	attempts := recs.all()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].HTTPStatus != http.StatusNotFound || attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("attempt = %d/%s, want 404/NO_RETRY", attempts[0].HTTPStatus, attempts[0].RetryDecision)
	}
}

// convert_failed, producer 1 (dispatch.go:456): buildHTTP rejects a base_url
// whose scheme is not http(s). channelProblem only rejects an empty value, so
// this is the smallest reachable construction.
func TestConvertFailedWhenBaseURLSchemeIsUnsupported(t *testing.T) {
	_, envelope := testBox(t)
	cat := testCatalog(t, "ftp://upstream.invalid/v1", envelope, nil)
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, &fakeLedger{}, recs, &fakeIdem{})

	rec := serve(t, d, testCtx(false))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeConvertFailed {
		t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeConvertFailed, rec.Body.String())
	}
	attempts := recs.all()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Phase != "P14" || attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("attempt phase/retry = %s/%s, want P14/NO_RETRY", attempts[0].Phase, attempts[0].RetryDecision)
	}
}

// convert_failed, producer 2 (dispatch.go:867): an OpenAI-shaped body without
// "choices" is preserved as _raw_response, which has no Anthropic
// representation. The inbound protocol is the only variable; the upstream
// itself answers 200.
func TestConvertFailedWhenResponseHasNoClientRepresentation(t *testing.T) {
	up := fixedUpstream(http.StatusOK,
		`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	recs := &attemptRecorder{}
	d := newDispatcher(t, cat, &fakeLedger{}, recs, &fakeIdem{})

	c := testCtx(false)
	c.Endpoint = gw.EndpointEmbeddings
	c.Protocol = gw.ProtocolAnthropic

	rec := serve(t, d, c)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec)["code"]; got != apierr.CodeConvertFailed {
		t.Fatalf("code = %v, want %s (body %s)", got, apierr.CodeConvertFailed, rec.Body.String())
	}
	attempts := recs.all()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].ErrorClass != ClassMalformedBody || attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("attempt = %s/%s, want %s/NO_RETRY",
			attempts[0].ErrorClass, attempts[0].RetryDecision, ClassMalformedBody)
	}
}

// client_aborted (dispatch.go:348): the client closes the connection before
// the upstream sends headers, so RoundTrip returns context.Canceled. The
// response cannot be written; the observation points are Ctx.ErrCode, the
// settle reason and the P28 usage row (checked here at the usage layer).
func TestClientAbortedMarksUsageRowAndSettle(t *testing.T) {
	arrived := make(chan struct{})
	var once sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Drain the body before waiting: only then does net/http start its
		// background read and cancel r.Context() when the client goes away.
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(arrived) })
		<-r.Context().Done()
	}))
	defer up.Close()

	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	led := &fakeLedger{}
	recs := &attemptRecorder{}
	usage := &usageRecorder{}
	d := newDispatcher(t, cat, led, recs, &fakeIdem{})
	d.opts.UsageSink = usage.add

	c := testCtx(false)
	c.Deadline = time.Now().Add(600 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		d.Serve(rec, req, c)
		close(done)
	}()

	// Deterministic ordering: the request has reached the upstream handler
	// before the context is cancelled. No sleep is involved.
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream request was never dispatched")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not return after the client abort")
	}

	if c.ErrCode != apierr.CodeClientAborted {
		t.Fatalf("ctx code = %q, want %s", c.ErrCode, apierr.CodeClientAborted)
	}
	if led.settleCount() != 1 || led.lastSettle().ReleaseReason != "CLIENT_ABORT" {
		t.Fatalf("settle = %+v, want one CLIENT_ABORT release", led.settles)
	}
	attempts := recs.all()
	if len(attempts) != 1 || attempts[0].ErrorClass != ClassClientAbort {
		t.Fatalf("attempts = %+v, want one %s", attempts, ClassClientAbort)
	}
	if attempts[0].RetryDecision != DecisionNoRetry {
		t.Fatalf("retry = %q, want NO_RETRY", attempts[0].RetryDecision)
	}

	rows := usage.all()
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.HTTPStatus != 499 || !row.ClientAborted {
		t.Fatalf("usage status/client_aborted = %d/%v, want 499/true", row.HTTPStatus, row.ClientAborted)
	}
	if row.FailStage != "CLIENT_ABORT" || row.FailOwner != "CLIENT" {
		t.Fatalf("usage attribution = %q/%q, want CLIENT_ABORT/CLIENT", row.FailStage, row.FailOwner)
	}
}

// Companion for idempotent_replay_unsupported: completeIdem (settle.go:106)
// drops a response body over maxReplayBody so the replay path can answer 409
// instead of a truncated body. The gw-side mapping test is
// TestIdempotentReplayWithoutStoredBodyIs409; this half proves the >64 KiB
// truncation that makes the stored record bodyless in the first place.
func TestLargeUnaryResponseIsNotStoredForReplay(t *testing.T) {
	large := strings.Repeat("x", maxReplayBody+4096)
	up := fixedUpstream(http.StatusOK,
		`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+
			large+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	defer up.Close()
	_, envelope := testBox(t)
	cat := testCatalog(t, up.URL+"/v1", envelope, nil)
	idem := &fakeIdem{}
	d := newDispatcher(t, cat, &fakeLedger{}, &attemptRecorder{}, idem)

	c := testCtx(false)
	c.Idem = &gw.IdemState{Record: idemstore.Record{RequestID: c.RequestID}}
	rec := serve(t, d, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(idem.completes) != 1 || idem.completes[0] != idemstore.StateDone {
		t.Fatalf("completes = %v, want [DONE]", idem.completes)
	}
	if len(idem.bodies) != 1 || len(idem.bodies[0]) != 0 {
		t.Fatalf("stored replay body length = %d, want 0 for a body over %d bytes",
			len(idem.bodies), maxReplayBody)
	}
}
