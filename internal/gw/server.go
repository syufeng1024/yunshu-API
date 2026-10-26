package gw

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/ratelimit"
)

// DefaultClientBudgetMS and DefaultStreamBudgetMS mirror the seeded
// gw.client_timeout_budget_ms / gw.stream_total_ms values. They are used only
// when the configuration store is absent.
const (
	DefaultClientBudgetMS = 120_000
	DefaultStreamBudgetMS = 600_000
)

// Terminal is the seam after P14. T07 ships NotWiredTerminal; T08 replaces it
// with the upstream dispatcher.
type Terminal interface {
	Serve(w http.ResponseWriter, r *http.Request, c *Ctx)
}

// TerminalFunc adapts a function to Terminal.
type TerminalFunc func(w http.ResponseWriter, r *http.Request, c *Ctx)

// Serve implements Terminal.
func (f TerminalFunc) Serve(w http.ResponseWriter, r *http.Request, c *Ctx) { f(w, r, c) }

// Handler is the data-plane entry point. It executes P01..P04 inline, then
// hands the request to the pipeline.
type Handler struct {
	d        *Deps
	pipeline *Pipeline
	authOnly *Pipeline
	terminal Terminal
}

// NewHandler wires the handler. A nil terminal gets NotWiredTerminal.
func NewHandler(d *Deps, terminal Terminal) *Handler {
	if terminal == nil {
		terminal = NotWiredTerminal(d)
	}
	return &Handler{
		d:        d,
		pipeline: NewDefaultPipeline(d),
		authOnly: NewAuthOnlyPipeline(d),
		terminal: terminal,
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := obslog.MetaFrom(r.Context()).RequestID
	if rid == "" {
		rid = w.Header().Get("X-Request-Id")
	}
	if rid == "" {
		rid = r.Header.Get("X-Request-Id")
	}
	if rid == "" {
		// Hard constraint 15: every response carries X-Request-Id, and it is
		// the same value as the body's request_id.
		rid = apierr.NewRequestID()
	}
	c := &Ctx{
		RequestID: rid,
		StartedAt: time.Now(),
		ConnID:    r.RemoteAddr,
		ClientIP:  clientIP(r.RemoteAddr),
		Header:    r.Header,
	}
	w.Header().Set("X-Request-Id", rid)

	// P01: the total request budget. Streaming is only known after P04, so the
	// non-stream budget is established first and upgraded below when the body
	// says stream=true. Both derive from the original request context, so the
	// longer budget still starts at P01.
	base := r.Context()
	ctx, cancel := context.WithTimeout(base, h.budget(false))
	defer cancel()

	// P03 runs before P02: an unknown path must not cost a 10 MB read. The
	// observable difference is only which error wins when both are wrong; the
	// card's A14 asserts the 404 for unknown paths.
	endpoint, protocol, apiErr := MatchEndpoint(r)
	if apiErr != nil {
		h.writeError(w, ctx, c, apiErr)
		return
	}
	c.Endpoint = endpoint
	c.Protocol = protocol

	// P02.
	if RequiresBody(endpoint) {
		body, apiErr := ReadBody(w, r, MaxBodyBytes, BodyReadTimeout)
		if apiErr != nil {
			h.writeError(w, ctx, c, apiErr)
			return
		}
		c.RawBody = body
		c.BodyHash = BodyHashOf(body)

		// P04.
		ir, apiErr := ParseRequest(endpoint, body)
		if apiErr != nil {
			h.writeError(w, ctx, c, apiErr)
			return
		}
		c.IR = ir
		c.Stream = ir.Stream
		if c.Stream {
			cancel()
			streamCtx, streamCancel := context.WithTimeout(base, h.budget(true))
			ctx = streamCtx
			defer streamCancel()
		}
	}
	if deadline, ok := ctx.Deadline(); ok {
		c.Deadline = deadline
	}
	// T08 consumes these two; reading them here keeps every upstream timeout
	// snapshot-driven instead of hard-coded in the future dispatcher.
	c.UpstreamTimeout = h.configMS(config.KeyGwUpstreamTimeoutMs, 50_000)
	c.FirstByteTimeout = h.configMS(config.KeyGwStreamFirstByteMs, 30_000)

	defer h.releaseRequest(ctx, c)

	var runErr error
	if endpoint == EndpointModels {
		runErr = h.authOnly.Run(ctx, c)
		if runErr == nil {
			h.writeModels(w, ctx, c)
			return
		}
	} else {
		runErr = h.pipeline.Run(ctx, c)
		if runErr == nil {
			if c.Replay != nil {
				// P08 stopped the pipeline with a cached result; the terminal
				// (upstream dispatch) must not run.
				h.writeReplay(w, ctx, c)
				return
			}
			h.terminal.Serve(w, r, c)
			return
		}
	}

	h.finishError(w, ctx, c, runErr)
}

// finishError maps the pipeline outcome to the wire.
func (h *Handler) finishError(w http.ResponseWriter, ctx context.Context, c *Ctx, err error) {
	if errors.Is(err, ErrConnectionReset) {
		ResetConnection(w)
		return
	}
	if c.Replay != nil {
		h.writeReplay(w, ctx, c)
		return
	}
	var se *StageError
	if errors.As(err, &se) {
		if se.API != nil {
			h.writeError(w, ctx, c, se.API)
			return
		}
		h.writeError(w, ctx, c, apierr.New(apierr.CodeInternalError).WithCause(se.Err))
		return
	}
	h.writeError(w, ctx, c, apierr.New(apierr.CodeInternalError).WithCause(err))
}

func (h *Handler) writeError(w http.ResponseWriter, ctx context.Context, c *Ctx, e *apierr.APIError) {
	e = e.WithRequestID(c.RequestID)
	if h.d.Logger != nil {
		fields := obslog.Fields{
			Stage:     c.FailStage,
			RequestID: c.RequestID,
			Status:    e.HTTPStatus,
		}
		h.d.Logger.Warn(ctx, "gateway_request_failed: "+e.Code, fields)
	}
	_ = apierr.Write(w, e)
}

// writeModels serves GET /v1/models. It consumes no quota: no backpressure
// slot, no rate-limit lease, no prehold (TDS §6.1).
func (h *Handler) writeModels(w http.ResponseWriter, ctx context.Context, c *Ctx) {
	snap, err := h.d.Catalog.Current()
	if err != nil {
		h.writeError(w, ctx, c, apierr.New(apierr.CodeConfigUnavailable).WithCause(err))
		return
	}
	models := snap.ListableModels(c.Auth.KeyID, c.Auth.GroupCode)
	data := make([]modelObject, 0, len(models))
	for _, m := range models {
		data = append(data, modelObject{
			ID:      m.ModelName,
			Object:  "model",
			Created: 0,
			OwnedBy: "yunshu",
			Type:    m.ModelType,
		})
	}
	payload, err := jsonx.Marshal(modelList{Object: "list", Data: data})
	if err != nil {
		h.writeError(w, ctx, c, apierr.New(apierr.CodeInternalError).WithCause(err))
		return
	}
	rid := apierr.SetResponseHeaders(w, apierr.ResponseMeta{RequestID: c.RequestID})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
	c.RequestID = rid
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Type    string `json:"type,omitempty"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

func (h *Handler) writeReplay(w http.ResponseWriter, ctx context.Context, c *Ctx) {
	decision := c.Replay
	if decision.Status == http.StatusOK && len(decision.Body) > 0 {
		rid := decision.RequestID
		if rid == "" {
			rid = c.RequestID
		}
		apierr.SetResponseHeaders(w, apierr.ResponseMeta{RequestID: rid})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(decision.Body)
		c.RequestID = rid
		return
	}
	code := apierr.CodeInternalError
	switch decision.Code {
	case "idempotency_key_reused":
		code = apierr.CodeIdempotencyKeyReused
	case "request_in_flight":
		code = apierr.CodeRequestInFlight
	case "stream_response_not_replayable", "response_not_replayable":
		code = apierr.CodeIdempotentReplayUnsupported
	}
	e := apierr.New(code)
	if code == apierr.CodeRequestInFlight {
		e.WithRetryAfter(2)
		// apierr.Write emits Retry-After for 402/429/503 only. TDS §4.5
		// requires it on the in-flight 409 as well, so the header is set here;
		// the body field is set through the error so the two cannot drift.
		w.Header().Set("Retry-After", "2")
	}
	h.writeError(w, ctx, c, e)
}

// releaseRequest is the single exit path for every resource the request may
// have acquired. It runs after the response is written.
func (h *Handler) releaseRequest(ctx context.Context, c *Ctx) {
	if c.Slot != nil {
		c.Slot.Release()
	}
	if c.RateAcquired && h.d.Limiter != nil {
		releaseCtx, cancel := detach(ctx, time.Second)
		defer cancel()
		estTokens := int64(0)
		estMicro := int64(0)
		if c.Est != nil {
			estTokens = ratelimit.PlaceholderTokens(
				c.Est.Tokens.InputTokens, c.Est.Tokens.OutputTokens, 0)
			estMicro = c.Est.EstMicro
		}
		nowMS := h.d.ClockNowMS()
		hadLease := false
		if c.Verdict != nil {
			hadLease = c.Verdict.ConcurrencyMode == ratelimit.ConcurrencyRedis
		}
		_, _ = h.d.Limiter.Release(releaseCtx, ratelimit.ReleaseInput{
			KeyID:           c.Auth.KeyID,
			UserID:          c.Auth.UserID,
			RequestID:       c.RequestID,
			AcquireMinEpoch: c.AcquiredMin,
			CurrentMinEpoch: minEpochOf(nowMS),
			Day:             ratelimit.Day(time.UnixMilli(nowMS)),
			TPMDelta:        -estTokens,
			DailyDelta:      -estMicro,
			HadLease:        hadLease,
		})
	}
	if c.ConnHeld && h.d.ConnGuard != nil {
		h.d.ConnGuard.Release(c.Auth.KeyID)
	}
	if c.Idem != nil && h.d.Idem != nil {
		// T07 never reaches a successful upstream response. A request that
		// owned the idempotency lease must be marked FAILED so the client's
		// retry is allowed instead of blocking for the 15s lease.
		completeCtx, cancel := detach(ctx, 2*time.Second)
		defer cancel()
		_ = h.d.Idem.Complete(completeCtx, c.Idem.Record, idemstore.StateFailed, nil)
	}
}

// detach keeps context values and drops cancellation, so an exit-path cleanup
// still runs after a client disconnect. It is not a detached background
// context: the trace/meta values survive.
func detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (h *Handler) configMS(key config.Key, def int64) time.Duration {
	ms := def
	if h.d.Config != nil {
		if v, ok := h.d.Config.Int(key); ok && v > 0 {
			ms = v
		}
	}
	return time.Duration(ms) * time.Millisecond
}

func (h *Handler) budget(stream bool) time.Duration {
	ms := int64(DefaultClientBudgetMS)
	if stream {
		ms = DefaultStreamBudgetMS
	}
	if h.d.Config != nil {
		key := config.KeyGwClientTimeoutBudget
		if stream {
			key = config.KeyGwStreamTotalMs
		}
		if v, ok := h.d.Config.Int(key); ok && v > 0 {
			ms = v
		}
	}
	return time.Duration(ms) * time.Millisecond
}

func clientIP(remoteAddr string) netip.Addr {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	if i := strings.IndexByte(host, '%'); i > 0 {
		host = host[:i]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// NotWiredTerminal is the T07 terminal. No upstream call is made in this card,
// so a hold that was placed must be released before answering; leaving it HELD
// would freeze customer funds until the 200s TTL, which is not a timeout case
// and must not be handed to reconciliation. T08 replaces this terminal with the
// dispatcher and removes the compensation.
func NotWiredTerminal(d *Deps) TerminalFunc {
	return func(w http.ResponseWriter, r *http.Request, c *Ctx) {
		if c.Hold != nil && d.Ledger != nil {
			ctx, cancel := detach(r.Context(), 5*time.Second)
			defer cancel()
			_, err := d.Ledger.Settle(ctx, ledger.SettleParams{
				RequestID:     c.RequestID,
				UserID:        c.Auth.UserID,
				ConsumedMicro: 0,
				ReleaseReason: "upstream_not_wired",
			})
			if err != nil && d.Logger != nil {
				d.Logger.Error(ctx, "prehold_release_failed: "+err.Error(), obslog.Fields{Stage: "P10"})
			}
		}
		e := apierr.New(apierr.CodeConfigUnavailable).WithRequestID(c.RequestID)
		if d.Logger != nil {
			d.Logger.Warn(r.Context(), "upstream_dispatch_not_wired", obslog.Fields{
				Stage:     "P14",
				RequestID: c.RequestID,
				Status:    e.HTTPStatus,
			})
		}
		_ = apierr.Write(w, e)
	}
}
