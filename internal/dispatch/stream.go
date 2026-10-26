package dispatch

import (
	"context"
	"errors"
	"net/http"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/gw"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/stream"
)

const (
	// Upstream frame idle timeout (TDS §4.5). A channel-level override is not
	// part of the D-14 payload; 30s is the platform value.
	streamIdleTimeout = 30 * time.Second
	// frameWriteTimeout bounds one client write/flush.
	frameWriteTimeout = 5 * time.Second
)

func (d *Dispatcher) streamAttempt(ctx context.Context, w http.ResponseWriter, c *gw.Ctx,
	t target, resp *http.Response, out outcome, start time.Time, secret string) outcome {
	proto, _ := protocolName(t.ch.ProtocolID)
	out.attempt.Phase = "P18"

	// The 30s first-byte budget covers headers + the first data frame. The
	// transport already spent part of it waiting for headers.
	firstByte := d.firstByteTimeout(t.ch) - time.Since(start)
	if firstByte <= 0 {
		firstByte = time.Millisecond
	}

	result, err := stream.Relay(ctx, resp.Body, stream.Options{
		ClientProtocol:    string(c.Protocol),
		UpstreamProtocol:  proto,
		W:                 w,
		RequestID:         c.RequestID,
		PromptTokens:      d.promptTokens(c),
		StartedAt:         c.StartedAt,
		PreheldMicro:      c.HoldMicro,
		RemainingCredits:  float64(d.availableBase(c) - c.HoldMicro),
		FirstByteTimeout:  firstByte,
		IdleTimeout:       streamIdleTimeout,
		HeartbeatInterval: stream.DefaultHeartbeatInterval,
		WriteTimeout:      frameWriteTimeout,
		Tokens:            d.opts.Tokens,
	})
	out.attempt.TotalMS = time.Since(start).Milliseconds()
	out.attempt.BytesOut = result.FlushedBytes
	if result.Usage.Source == "" {
		result.Usage = stream.ResolveUsage(nil, d.promptTokens(c), "", d.opts.Tokens)
	}
	out.tokenIn = result.Usage.TokensIn
	out.tokenOut = result.Usage.TokensOut
	out.attempt.TokensIn = result.Usage.TokensIn
	out.attempt.TokensOut = result.Usage.TokensOut

	if !result.Started {
		if result.ClientAborted || errors.Is(err, stream.ErrClientAbort) {
			out.clientAborted = true
			out.class = ClassClientAbort
			out.attempt.ErrorClass = ClassClientAbort
			return out
		}
		out.class, out.apiErr = classifyStreamStartError(err)
		out.attempt.ErrorClass = out.class
		out.attempt.Phase = "P16"
		if err != nil {
			out.attempt.UpstreamMsg = sanitizeUpstreamMsg(err.Error(), secret)
		}
		out.result = "FAILED"
		return out
	}

	consumed, costErr := d.cost(c, result.Usage)
	if costErr != nil {
		// The stream is already on the wire; the charge cannot be recomputed.
		if d.opts.Logger != nil {
			d.opts.Logger.Error(ctx, "stream_cost_failed: "+costErr.Error(), obslog.Fields{
				Stage: "P25", RequestID: c.RequestID,
			})
		}
		consumed = 0
	}
	reason := ""
	if consumed == 0 {
		if result.ClientAborted {
			reason = "CLIENT_ABORT"
		} else {
			reason = "UPSTREAM_FAIL"
		}
	}
	if _, settleErr := d.settle(ctx, c, consumed, reason); settleErr != nil && d.opts.Logger != nil {
		d.opts.Logger.Error(ctx, "stream_settle_failed: "+settleErr.Error(), obslog.Fields{
			Stage: "P27", RequestID: c.RequestID,
		})
	}
	d.completeIdem(ctx, c, nil)
	d.noteUsage(ctx, t.ch.ID, result.Usage.UpstreamOK, t.ch.StreamUsageMode)

	if result.ClientAborted {
		out.result = "PARTIAL"
		if consumed == 0 && result.Usage.TokensIn == 0 && result.Usage.TokensOut == 0 {
			out.result = "FAILED"
		}
		out.finished = true
		return out
	}

	if result.NeedErrorFrame {
		api := streamFailureAPI(result)
		api = api.WithRequestID(c.RequestID)
		credits := consumed
		api.CreditsConsumed = &credits
		if writeErr := apierr.WriteSSEFlush(w, api, sseProtocol(c.Protocol)); writeErr != nil && d.opts.Logger != nil {
			d.opts.Logger.Warn(ctx, "stream_error_frame_failed: "+writeErr.Error(), obslog.Fields{
				Stage: "P24", RequestID: c.RequestID,
			})
		}
		out.result = "PARTIAL"
		if consumed == 0 {
			out.result = "FAILED"
		}
		out.attempt.ErrorClass = ClassTimeout
		if errors.Is(result.LastError, stream.ErrMalformedFrame) || errors.Is(result.LastError, stream.ErrUpstreamEvent) {
			out.attempt.ErrorClass = ClassMalformedBody
		}
		out.finished = true
		return out
	}

	out.result = "SUCCESS"
	out.finished = true
	return out
}

func classifyStreamStartError(err error) (string, *apierr.APIError) {
	switch {
	case errors.Is(err, stream.ErrBeforeFirstByte):
		return ClassTimeout, apierr.New(apierr.CodeUpstreamTimeout)
	case errors.Is(err, stream.ErrUpstreamEmpty):
		return ClassEmptyChoices, apierr.New(apierr.CodeUpstreamEmptyResponse)
	case errors.Is(err, stream.ErrMalformedFrame):
		return ClassMalformedBody, apierr.New(apierr.CodeUpstreamInvalidResponse)
	case errors.Is(err, stream.ErrUpstreamEvent):
		return ClassMalformedBody, apierr.New(apierr.CodeUpstreamInvalidResponse)
	case errors.Is(err, stream.ErrClientAbort):
		return ClassClientAbort, apierr.New(apierr.CodeClientAborted)
	default:
		if errors.Is(err, context.DeadlineExceeded) {
			return ClassTimeout, apierr.New(apierr.CodeUpstreamTimeout)
		}
		return ClassConnectRefused, apierr.New(apierr.CodeUpstreamAllFailed)
	}
}

func streamFailureAPI(result stream.Result) *apierr.APIError {
	switch {
	case errors.Is(result.LastError, stream.ErrMalformedFrame),
		errors.Is(result.LastError, stream.ErrUpstreamEvent):
		return apierr.New(apierr.CodeUpstreamInvalidResponse).WithRetryAfter(10)
	case errors.Is(result.LastError, stream.ErrUpstreamIdle),
		errors.Is(result.LastError, stream.ErrUpstreamEmpty):
		return apierr.New(apierr.CodeUpstreamTimeout).WithRetryAfter(30)
	default:
		return apierr.New(apierr.CodeUpstreamAllFailed).WithRetryAfter(30)
	}
}

func sseProtocol(p gw.Protocol) apierr.SSEProtocol {
	if p == gw.ProtocolAnthropic {
		return apierr.SSEAnthropic
	}
	return apierr.SSEOpenAI
}
