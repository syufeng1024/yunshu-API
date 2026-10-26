// Package gw implements the inbound half of the gateway pipeline: P01 body
// ingress through P14. Stage identifiers are the fixed P01..P31 labels from
// TDS §3.1; the execution order inside Pipeline is NOT the numbering order.
//
// The package never opens a primary-database pool of its own. Every DB write
// goes through the injected ledger / idemstore store, both of which the caller
// wires to the restricted gw_write_dsn channel (8 connections, 200ms txn cap).
// Authentication and routing are served from Redis snapshots only; a K13 miss
// is a 401, never a database fallback.
package gw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/netip"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/billing"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/ratelimit"
	"yunshu.dev/relaykit/ir"
)

// Endpoint identifies the inbound protocol route (P03).
type Endpoint string

// The five public data-plane endpoints (TDS §6.1 API-01..API-05).
const (
	EndpointChat       Endpoint = "/v1/chat/completions"
	EndpointMessages   Endpoint = "/v1/messages"
	EndpointImages     Endpoint = "/v1/images/generations"
	EndpointEmbeddings Endpoint = "/v1/embeddings"
	EndpointModels     Endpoint = "/v1/models"
)

// Protocol is the inbound wire protocol recognized at P03.
type Protocol string

// Inbound protocols. Only these two are exposed in V1.0 (D-11).
const (
	ProtocolOpenAI    Protocol = "OPENAI_COMPAT"
	ProtocolAnthropic Protocol = "ANTHROPIC_COMPAT"
)

// AuthContext is the P05 output. It carries only what the snapshot can prove:
// account status and overdraft live in a separate cache (TDS §3.2 P05 note),
// so the gateway never fabricates them here.
type AuthContext struct {
	UserID       int64
	KeyID        int64
	KeyName      string
	GroupCode    string
	GroupRatioBP int64
	IPMRuleVer   int64
	KeyExpiryMS  int64
}

// IdemState is the P08 lease this request owns. Only an owned record may be
// completed; a replay never reaches the pipeline.
type IdemState struct {
	Key    string
	Hash   string
	Record idemstore.Record
}

// Ctx is the per-request state shared by every stage. It is created once per
// HTTP request and discarded at the end; nothing in it is shared between
// requests.
type Ctx struct {
	// Identity and transport.
	RequestID string
	StartedAt time.Time
	Deadline  time.Time
	Stream    bool

	Endpoint Endpoint
	Protocol Protocol
	ClientIP netip.Addr
	ConnID   string
	Header   http.Header

	RawBody  []byte
	BodyHash string
	IR       *ir.Request

	// Stage outputs.
	Auth  *AuthContext
	Model *ModelEntry
	Idem  *IdemState
	// IdemDone is set by the terminal after a successful/partial response was
	// recorded. releaseRequest must not overwrite it with FAILED.
	IdemDone bool
	Replay   *idemstore.ReplayDecision
	Slot     *InflightSlot
	Route    *RouteCandidate
	Routes   []RouteCandidate
	Est      *billing.Preestimate
	Price    *PriceRule
	Verdict  *ratelimit.Verdict
	Hold     *ledger.PreholdResult

	// Upstream timeouts copied from the snapshot for T08 (P15/P16).
	UpstreamTimeout  time.Duration
	FirstByteTimeout time.Duration

	// Numbers the exit path needs for release/refund.
	AvailableMicro int64
	RawEstMicro    int64
	HoldMicro      int64
	LeaseMS        int64
	AcquiredMin    int64
	RateAcquired   bool
	ConnHeld       bool
	Limits         Limits

	// FailStage is the P-number that produced the error. For the six-slot
	// pipeline it is the inner stage (e.g. P07) even though the slot is P05.
	FailStage string
}

// BodyHashOf returns the hex SHA-256 of the request body. It is the idempotency
// comparison value and is never logged.
func BodyHashOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// StageError carries a stage identifier and the API error that must be written.
// A stage that returns a bare Go error is a platform fault and is normalized to
// internal_error by the pipeline.
type StageError struct {
	Stage string
	API   *apierr.APIError
	Err   error
}

func (e *StageError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.API != nil {
		return e.Stage + ": " + e.API.Code
	}
	if e.Err != nil {
		return e.Stage + ": " + e.Err.Error()
	}
	return e.Stage
}

func (e *StageError) Unwrap() error { return e.Err }

// StageErr builds a StageError from an API error.
func StageErr(stage string, e *apierr.APIError) error {
	return &StageError{Stage: stage, API: e}
}

// StageWrap wraps an internal error. It is logged but never shown verbatim.
func StageWrap(stage string, err error) error {
	return &StageError{Stage: stage, Err: err}
}

// ErrPipelineStop stops the pipeline without an error. It is used by P08 when a
// replay has already produced the response.
var ErrPipelineStop = pipelineStop{}

type pipelineStop struct{}

func (pipelineStop) Error() string { return "pipeline: stop" }

// ErrConnectionReset asks the server to reset the TCP connection instead of
// writing an HTTP response. P01 raises it when the per-key connection cap is
// exceeded (TDS §3.1: RST, not 429).
var ErrConnectionReset = connectionReset{}

type connectionReset struct{}

func (connectionReset) Error() string { return "pipeline: connection reset" }

// contextDone maps a canceled request context to the client_aborted code. It
// is called between stages; I/O calls surface their own context errors.
func contextDone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return StageErr("P24", apierr.New(apierr.CodeClientAborted))
	}
	return nil
}
