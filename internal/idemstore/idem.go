// Package idemstore implements the K6/K7 idempotency fast path.
//
// Ordering is fixed by D-06/T06 hard constraint 17: the durable
// t_idempotency_record row is written first, Redis second. Redis is a cache:
// if its write fails the request continues (performance degrades), because
// the DB uniqueness key (scope, api_key_id, idem_key) is still the authority.
// The opposite order would make a Redis flush able to lose a completed
// request while the DB has no row to fall back to.
package idemstore

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	log "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/redisx"
)

//go:embed scripts/idem_renew.lua
var renewScriptSource string

var renewScript = goredis.NewScript(renewScriptSource)

// State is the durable idempotency state.
type State string

// States. There is no "SUCCESS" state: DONE means the response is durable in
// t_idempotency_record, not that the upstream returned 2xx.
const (
	StateInFlight State = "IN_FLIGHT"
	StateDone     State = "DONE"
	StateFailed   State = "FAILED"
)

// Action is the Redis/L4 action code. The numeric values match TDS §2.2 L4.
type Action int

const (
	ActionNew             Action = 1
	ActionReplay          Action = 2
	ActionWait            Action = 3
	ActionRetryPrevFailed Action = 4
	ActionStaleLock       Action = 5
	ActionKeyReused       Action = 6
)

// Errors surfaced to the caller. ErrFenceInvalid means another worker took
// the lease; it must not be retried with the old fence.
var (
	ErrRecordNotFound = errors.New("idemstore: record not found")
	ErrFenceInvalid   = errors.New("idemstore: stale fence")
)

// Record is one row of t_idempotency_record plus the cached response body.
type Record struct {
	Scope    string
	APIKeyID int64
	UserID   int64
	IdemKey  string
	BodyHash string

	State      State
	RequestID  string
	FenceToken string

	LeaseExpiresAt time.Time
	CreatedAt      time.Time
	ResponseBody   []byte
}

// DB is the authoritative store. Begin must be atomic: insert IN_FLIGHT when
// the unique key is absent, otherwise return the existing row with
// inserted=false. Takeover must CAS on oldFence and rotate the lease/fence.
type DB interface {
	Begin(ctx context.Context, rec Record) (existing *Record, inserted bool, err error)
	Get(ctx context.Context, scope string, apiKeyID int64, idemKey string) (*Record, error)
	Complete(ctx context.Context, rec Record, state State, body []byte) error
	Renew(ctx context.Context, rec Record, until time.Time) error
	Takeover(ctx context.Context, rec Record, oldFence string) (bool, error)
}

// Options are the idem.* configuration values (TDS §1.6.1).
type Options struct {
	LeaseTTL        time.Duration // idem.lease_ttl_seconds, 15s
	RenewEvery      time.Duration // idem.lease_renew_seconds, 5s
	DuplicateWindow time.Duration // idem.duplicate_window_seconds, 10s
}

// DefaultOptions returns the frozen V1.0 values.
func DefaultOptions() Options {
	return Options{
		LeaseTTL:        15 * time.Second,
		RenewEvery:      5 * time.Second,
		DuplicateWindow: 10 * time.Second,
	}
}

// BeginInput is one idempotency admission.
type BeginInput struct {
	Scope    string
	APIKeyID int64
	UserID   int64
	IdemKey  string
	BodyHash string

	RequestID  string
	FenceToken string // caller supplies a ULID; empty generates 128 random bits
}

// BeginResult is what the gateway needs to decide the response.
type BeginResult struct {
	Action     Action
	Record     *Record
	RetryAfter time.Duration
}

// Store is the K6/K7 cache in front of DB.
type Store struct {
	client *redisx.Client
	clock  *redisx.Clock
	db     DB
	opts   Options
	logger redisx.Logger
}

// New builds a store. clock may be nil only when every call uses the real
// clock; DB is mandatory.
func New(client *redisx.Client, clock *redisx.Clock, db DB, opts Options, logger redisx.Logger) *Store {
	if clock == nil {
		clock = redisx.NewClock(nil, nil)
	}
	if opts.LeaseTTL <= 0 {
		opts = DefaultOptions()
	}
	return &Store{client: client, clock: clock, db: db, opts: opts, logger: logger}
}

// Begin decides NEW / REPLAY / WAIT / RETRY_PREV_FAILED / STALE_LOCK /
// KEY_REUSED. A Redis failure never turns into a request failure: the DB path
// runs and the cache is refreshed best-effort.
func (s *Store) Begin(ctx context.Context, in BeginInput) (BeginResult, error) {
	if in.Scope == "" || in.IdemKey == "" || in.BodyHash == "" {
		return BeginResult{}, errors.New("idemstore: scope, idem_key and body_hash are required")
	}
	if in.RequestID == "" {
		return BeginResult{}, errors.New("idemstore: request_id is required")
	}
	if in.FenceToken == "" {
		in.FenceToken = generateFence()
	}
	hash := redisx.IdemHash(in.Scope, in.APIKeyID, in.IdemKey)
	now := s.clock.Now()

	// Redis read fast path. It can reject a reused key and answer WAIT
	// without touching the DB; every other outcome falls through to the
	// authority so a stale cache cannot decide the request.
	if cached, ok := s.readCached(ctx, in.UserID, hash); ok {
		switch cached.state {
		case StateDone:
			// The body lives only in the DB; DONE in Redis still needs one
			// read. A missing DB row means cache pollution.
			rec, err := s.db.Get(ctx, in.Scope, in.APIKeyID, in.IdemKey)
			if err == nil && rec != nil {
				if rec.BodyHash != in.BodyHash {
					return BeginResult{Action: ActionKeyReused, Record: rec}, nil
				}
				return BeginResult{Action: ActionReplay, Record: rec}, nil
			}
			if err != nil && !errors.Is(err, ErrRecordNotFound) {
				return BeginResult{}, err
			}
			s.deleteCached(ctx, in.UserID, hash)
		case StateFailed:
			// Fall through to DB: it decides whether the retry is allowed.
		case StateInFlight:
			if cached.bodyHash != "" && cached.bodyHash != in.BodyHash {
				return BeginResult{Action: ActionKeyReused}, nil
			}
			acquired, err := s.takeLock(ctx, in.UserID, hash, in.RequestID)
			if err == nil && !acquired {
				return BeginResult{Action: ActionWait, RetryAfter: time.Second}, nil
			}
			if acquired {
				// The value is a stale IN_FLIGHT. Drop the provisional lock
				// and let the DB takeover decide; cacheBegin re-acquires it
				// only if the fence rotation succeeds.
				s.deleteLock(ctx, in.UserID, hash)
			}
		}
	}

	rec := Record{
		Scope:          in.Scope,
		APIKeyID:       in.APIKeyID,
		UserID:         in.UserID,
		IdemKey:        in.IdemKey,
		BodyHash:       in.BodyHash,
		State:          StateInFlight,
		RequestID:      in.RequestID,
		FenceToken:     in.FenceToken,
		LeaseExpiresAt: now.Add(s.opts.LeaseTTL),
		CreatedAt:      now,
	}

	existing, inserted, err := s.db.Begin(ctx, rec)
	if err != nil {
		return BeginResult{}, err
	}
	if inserted {
		s.cacheBegin(ctx, in.UserID, hash, rec)
		return BeginResult{Action: ActionNew, Record: &rec}, nil
	}
	if existing == nil {
		return BeginResult{}, errors.New("idemstore: DB reported conflict without a row")
	}
	if existing.BodyHash != in.BodyHash {
		return BeginResult{Action: ActionKeyReused, Record: existing}, nil
	}
	switch existing.State {
	case StateDone:
		return BeginResult{Action: ActionReplay, Record: existing}, nil
	case StateFailed:
		// The previous attempt failed and may be retried with this request's
		// request_id/fence. Rotate the row now; otherwise every retry would
		// keep reading FAILED and never become a live attempt.
		taken, err := s.db.Takeover(ctx, rec, existing.FenceToken)
		if err != nil {
			return BeginResult{}, err
		}
		if !taken {
			return BeginResult{Action: ActionWait, RetryAfter: time.Second, Record: existing}, nil
		}
		s.cacheBegin(ctx, in.UserID, hash, rec)
		return BeginResult{Action: ActionRetryPrevFailed, Record: &rec}, nil
	case StateInFlight:
		if existing.LeaseExpiresAt.After(now) {
			return BeginResult{Action: ActionWait, RetryAfter: time.Second, Record: existing}, nil
		}
		taken, err := s.db.Takeover(ctx, rec, existing.FenceToken)
		if err != nil {
			return BeginResult{}, err
		}
		if !taken {
			return BeginResult{Action: ActionWait, RetryAfter: time.Second, Record: existing}, nil
		}
		s.cacheBegin(ctx, in.UserID, hash, rec)
		return BeginResult{Action: ActionStaleLock, Record: &rec}, nil
	default:
		return BeginResult{}, fmt.Errorf("idemstore: unknown state %q", existing.State)
	}
}

// Complete persists the terminal state to the DB first, then updates Redis.
// A Redis error is logged and ignored: the DB row is already the truth.
// The fence must match the row; otherwise ErrFenceInvalid is returned and the
// Redis value is left untouched.
func (s *Store) Complete(ctx context.Context, rec Record, state State, body []byte) error {
	if state != StateDone && state != StateFailed {
		return fmt.Errorf("idemstore: Complete with non-terminal state %q", state)
	}
	if err := s.db.Complete(ctx, rec, state, body); err != nil {
		return err
	}
	hash := redisx.IdemHash(rec.Scope, rec.APIKeyID, rec.IdemKey)
	ttl := s.cacheTTL()
	value := encodeValue(state, rec.RequestID, rec.CreatedAt.UnixMilli(), rec.BodyHash, rec.FenceToken)

	ctx, cancel := s.client.CommandContext(ctx)
	defer cancel()
	pipe := s.client.Raw().Pipeline()
	pipe.Set(ctx, redisx.IdemKey(rec.UserID, hash), value, ttl)
	pipe.Del(ctx, redisx.IdemLockKey(rec.UserID, hash))
	if _, err := pipe.Exec(ctx); err != nil && s.logger != nil {
		s.logger.Warn(ctx, "idem_cache_write_failed: "+err.Error(), log.Fields{Stage: "idempotency"})
	}
	return nil
}

// Renew extends the lease every idem.lease_renew_seconds. The DB CAS runs
// first; a stale fence is rejected there before any Redis lease is extended.
func (s *Store) Renew(ctx context.Context, rec Record) error {
	until := s.clock.Now().Add(s.opts.LeaseTTL)
	if err := s.db.Renew(ctx, rec, until); err != nil {
		return err
	}
	hash := redisx.IdemHash(rec.Scope, rec.APIKeyID, rec.IdemKey)
	keys := []string{redisx.IdemKey(rec.UserID, hash), redisx.IdemLockKey(rec.UserID, hash)}
	lctx, cancel := s.client.LuaContext(ctx)
	defer cancel()
	if err := renewScript.Run(lctx, s.client.Raw(), keys,
		rec.RequestID, s.opts.LeaseTTL.Milliseconds()).Err(); err != nil && s.logger != nil {
		s.logger.Warn(ctx, "idem_cache_renew_failed: "+err.Error(), log.Fields{Stage: "idempotency"})
	}
	return nil
}

// DecideReplay maps a hit to the HTTP contract. stream=true cannot replay a
// body that was never buffered, so it is a 409; non-stream returns the cached
// 200 body. A reused key is a 400 in both cases.
type ReplayDecision struct {
	Status     int
	Code       string
	Body       []byte
	RequestID  string
	RetryAfter time.Duration
}

// DecideReplay implements T06 hard constraint 18.
func DecideReplay(result BeginResult, stream bool) ReplayDecision {
	switch result.Action {
	case ActionReplay:
		requestID := ""
		if result.Record != nil {
			requestID = result.Record.RequestID
		}
		if stream {
			return ReplayDecision{Status: 409, Code: "stream_response_not_replayable", RequestID: requestID}
		}
		var body []byte
		if result.Record != nil {
			body = result.Record.ResponseBody
		}
		if len(body) == 0 {
			// Stream responses are stored without a body (TDS §1.6). Replaying
			// one as 200 would send an empty body, so treat it as
			// non-replayable even when the retry asks for non-stream.
			return ReplayDecision{Status: 409, Code: "response_not_replayable", RequestID: requestID}
		}
		return ReplayDecision{Status: 200, Body: body, RequestID: requestID}
	case ActionWait:
		return ReplayDecision{Status: 409, Code: "request_in_flight", RetryAfter: time.Second}
	case ActionKeyReused:
		return ReplayDecision{Status: 400, Code: "idempotency_key_reused"}
	case ActionNew, ActionRetryPrevFailed, ActionStaleLock:
		// Continue down the normal pipeline. RETRY_PREV_FAILED means the
		// previous attempt failed and this attempt may proceed; STALE_LOCK
		// means this attempt now owns the lease.
		return ReplayDecision{}
	default:
		return ReplayDecision{Status: 500, Code: "idempotency_state_unknown"}
	}
}

func (s *Store) cacheBegin(ctx context.Context, userID int64, hash string, rec Record) {
	value := encodeValue(StateInFlight, rec.RequestID, rec.CreatedAt.UnixMilli(), rec.BodyHash, rec.FenceToken)
	cctx, cancel := s.client.CommandContext(ctx)
	defer cancel()
	pipe := s.client.Raw().Pipeline()
	pipe.Set(cctx, redisx.IdemKey(userID, hash), value, s.cacheTTL())
	pipe.Set(cctx, redisx.IdemLockKey(userID, hash), rec.RequestID, s.opts.LeaseTTL)
	if _, err := pipe.Exec(cctx); err != nil && s.logger != nil {
		// D-06: cache failure only costs a DB round trip on the next hit.
		s.logger.Warn(cctx, "idem_cache_write_failed: "+err.Error(), log.Fields{Stage: "idempotency"})
	}
}

func (s *Store) takeLock(ctx context.Context, userID int64, hash, requestID string) (bool, error) {
	cctx, cancel := s.client.CommandContext(ctx)
	defer cancel()
	return s.client.Raw().SetNX(cctx, redisx.IdemLockKey(userID, hash),
		requestID, s.opts.LeaseTTL).Result()
}

func (s *Store) deleteLock(ctx context.Context, userID int64, hash string) {
	cctx, cancel := s.client.CommandContext(ctx)
	defer cancel()
	_ = s.client.Raw().Del(cctx, redisx.IdemLockKey(userID, hash)).Err()
}

func (s *Store) deleteCached(ctx context.Context, userID int64, hash string) {
	cctx, cancel := s.client.CommandContext(ctx)
	defer cancel()
	_ = s.client.Raw().Del(cctx, redisx.IdemKey(userID, hash)).Err()
}

type cachedValue struct {
	state     State
	requestID string
	created   int64
	bodyHash  string
	fence     string
}

func (s *Store) readCached(ctx context.Context, userID int64, hash string) (cachedValue, bool) {
	cctx, cancel := s.client.CommandContext(ctx)
	defer cancel()
	raw, err := s.client.Raw().Get(cctx, redisx.IdemKey(userID, hash)).Result()
	if err != nil {
		if !errors.Is(err, goredis.Nil) && s.logger != nil {
			// Redis down: use the DB. This is the documented degradation.
			s.logger.Warn(ctx, "idem_cache_read_failed: "+err.Error(), log.Fields{Stage: "idempotency"})
		}
		return cachedValue{}, false
	}
	value, ok := decodeValue(raw)
	if !ok {
		s.deleteCached(ctx, userID, hash)
		return cachedValue{}, false
	}
	return value, true
}

func encodeValue(state State, requestID string, createdMS int64, bodyHash, fence string) string {
	return strings.Join([]string{
		string(state), requestID, strconv.FormatInt(createdMS, 10), bodyHash, fence,
	}, "|")
}

func decodeValue(raw string) (cachedValue, bool) {
	parts := strings.Split(raw, "|")
	if len(parts) < 3 {
		return cachedValue{}, false
	}
	created, _ := strconv.ParseInt(parts[2], 10, 64)
	value := cachedValue{state: State(parts[0]), requestID: parts[1], created: created}
	if len(parts) > 3 {
		value.bodyHash = parts[3]
	}
	if len(parts) > 4 {
		value.fence = parts[4]
	}
	switch value.state {
	case StateDone, StateFailed, StateInFlight:
		return value, true
	default:
		return cachedValue{}, false
	}
}

func (s *Store) cacheTTL() time.Duration {
	// 24h + up to 300s of jitter so a cache-wide event cannot expire every
	// idempotency key in the same second.
	return 24*time.Hour + time.Duration(mathrand.IntN(300))*time.Second
}

// generateFence returns a UUIDv7-shaped token (36 characters) so it fits
// t_idempotency_record.fence_token char(36) without padding. The 48-bit prefix
// keeps tokens roughly ordered by acquisition time; the remaining bits are
// random, so a fence is never reused across acquisitions.
func generateFence() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand failure is unrecoverable for a fence; the clock keeps the
		// value unique enough for a single process.
		ns := uint64(time.Now().UnixNano())
		for i := 6; i < 16; i++ {
			b[i] = byte(ns >> (8 * uint(i-6)))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
