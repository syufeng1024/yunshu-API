package gw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/billing"
	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/idemstore"
	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/ratelimit"
	"yunshu.dev/core/internal/redisx"
)

// TestIntegrationAuthReadsK13 uses the real Redis cache: the gateway has no
// database fallback, so a missing HASH must be a 401.
func TestIntegrationAuthReadsK13(t *testing.T) {
	client := newRedisClient(t)
	cache := redisx.NewKeyAuthCache(client)
	key := "sk-integration-" + randomID(t)
	if err := cache.Upsert(t.Context(), KeyHash16(key), redisx.KeyAuthRecord{
		UID: 7, KID: 10, ST: "ENABLED", GRP: "contract", GP: 8500,
	}); err != nil {
		t.Fatalf("upsert K13: %v", err)
	}

	deps := staticDeps(nil)
	deps.Auth = cache
	c := ctxWithAuthHeader(key)
	if err := NewAuthStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if c.Auth.UserID != 7 || c.Auth.GroupCode != "contract" || c.Auth.GroupRatioBP != 8500 {
		t.Fatalf("auth = %+v", c.Auth)
	}

	miss := ctxWithAuthHeader("sk-not-in-k13-" + randomID(t))
	err := NewAuthStage(deps).Run(t.Context(), miss)
	assertAPIError(t, err, apierr.CodeInvalidAPIKey, http.StatusUnauthorized)
}

// TestA8RateLimit429Headers uses the real Lua limiter.
func TestA8RateLimit429Headers(t *testing.T) {
	client := newRedisClient(t)
	clock := redisx.NewClock(nil, nil)
	limiter := ratelimit.New(client, clock, nil, nil, nil)
	keyID := time.Now().UnixNano() & 0x7fffffff
	userID := keyID + 1

	deps := staticDeps(nil)
	deps.Limiter = limiter
	deps.CounterReader = RedisCounters{Client: client}
	deps.Clock = clock
	deps.Config = nil

	limits := Limits{RPM: 1, TPM: 10_000_000, Concurrency: 100,
		DailyQuotaMicro: 1_000_000, DailyEnabled: true}
	estValue := billingPreestimateForTest()
	est := &estValue

	first := &Ctx{
		RequestID: "rl-first-" + randomID(t),
		Auth:      &AuthContext{UserID: userID, KeyID: keyID, GroupCode: "standard", GroupRatioBP: 10000},
		Limits:    limits,
		Est:       est,
	}
	stage := NewRateLimitStage(deps)
	if err := stage.Run(t.Context(), first); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !first.RateAcquired {
		t.Fatal("first acquire did not set RateAcquired")
	}

	second := &Ctx{
		RequestID: "rl-second-" + randomID(t),
		Auth:      &AuthContext{UserID: userID, KeyID: keyID, GroupCode: "standard", GroupRatioBP: 10000},
		Limits:    limits,
		Est:       est,
	}
	err := stage.Run(t.Context(), second)
	assertAPIError(t, err, apierr.CodeRPMLimitExceeded, http.StatusTooManyRequests)

	var se *StageError
	if errAs(err, &se) {
		e := se.API
		if e.RetryAfterSeconds <= 0 {
			t.Fatalf("retry_after = %d", e.RetryAfterSeconds)
		}
		reset, parseErr := time.Parse(time.RFC3339, e.ResetAt)
		if parseErr != nil {
			t.Fatalf("reset_at %q: %v", e.ResetAt, parseErr)
		}
		nowSec := time.Now().Unix()
		if got, want := e.RetryAfterSeconds, int(reset.Unix()-nowSec); absInt(got-want) > 1 {
			t.Fatalf("retry_after=%d but reset_at implies %d", got, want)
		}

		rec := httptest.NewRecorder()
		if writeErr := apierr.Write(rec, e.WithRequestID("rid-rl")); writeErr != nil {
			t.Fatalf("write: %v", writeErr)
		}
		res := rec.Result()
		defer res.Body.Close()
		if got := res.Header.Get("Retry-After"); got != strconv.Itoa(e.RetryAfterSeconds) {
			t.Fatalf("Retry-After = %q, want %d", got, e.RetryAfterSeconds)
		}
		if res.Header.Get("X-RateLimit-Limit-Requests") == "" ||
			res.Header.Get("X-RateLimit-Remaining-Requests") == "" ||
			res.Header.Get("X-RateLimit-Reset-Requests") == "" {
			t.Fatalf("missing RPM headers: %v", res.Header)
		}
		if res.Header.Get("X-Quota-Daily-Limit") == "" ||
			res.Header.Get("X-Quota-Daily-Remaining") == "" {
			t.Fatalf("missing daily header group: %v", res.Header)
		}
	}

	// Release the first request's lease so the fixture does not leak into a
	// later run of the same binary.
	_, _ = limiter.Release(t.Context(), ratelimit.ReleaseInput{
		KeyID: keyID, UserID: userID, RequestID: first.RequestID,
		AcquireMinEpoch: first.AcquiredMin, CurrentMinEpoch: minEpochOf(clock.NowMS()),
		Day: ratelimit.Day(clock.Now()), TPMDelta: -est.Tokens.InputTokens,
		HadLease: true,
	})
}

// TestIntegrationIdempotencyReplay drives the real Redis + MySQL store.
func TestIntegrationIdempotencyReplay(t *testing.T) {
	client := newRedisClient(t)
	clock := redisx.NewClock(nil, nil)
	store := idemstore.New(client, clock, idemstore.NewMySQL(testDB), idemstore.DefaultOptions(), nil)

	idemKey := "idem-" + randomID(t)
	apiKeyID := time.Now().UnixNano() & 0x7fffffff
	mustExec(t, `DELETE FROM t_idempotency_record WHERE scope = 'gateway' AND api_key_id = ? AND idem_key = ?`,
		apiKeyID, idemKey)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.WithoutCancel(t.Context()),
			`DELETE FROM t_idempotency_record WHERE scope = 'gateway' AND api_key_id = ? AND idem_key = ?`,
			apiKeyID, idemKey)
	})

	deps := staticDeps(nil)
	deps.Idem = store
	deps.Auth = fakeAuth{records: map[string]redisx.KeyAuthRecord{}}
	stage := NewIdemStage(deps)

	header := http.Header{}
	header.Set("Idempotency-Key", idemKey)
	first := &Ctx{
		RequestID: "idem-first-" + randomID(t),
		Header:    header,
		RawBody:   []byte(`{"model":"m","messages":[]}`),
		Auth:      &AuthContext{UserID: 7, KeyID: apiKeyID},
	}
	if err := stage.Run(t.Context(), first); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if first.Idem == nil {
		t.Fatal("first request did not own the idempotency lease")
	}
	if err := store.Complete(t.Context(), first.Idem.Record, idemstore.StateDone, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}

	second := &Ctx{
		RequestID: "idem-second-" + randomID(t),
		Header:    header,
		RawBody:   []byte(`{"model":"m","messages":[]}`),
		Auth:      &AuthContext{UserID: 7, KeyID: apiKeyID},
	}
	err := stage.Run(t.Context(), second)
	if err != ErrPipelineStop {
		t.Fatalf("replay err = %v, want ErrPipelineStop", err)
	}
	if second.Replay == nil || second.Replay.Status != 200 {
		t.Fatalf("replay = %+v", second.Replay)
	}
	if strings.TrimSpace(string(second.Replay.Body)) != `{"ok":true}` {
		t.Fatalf("replay body = %s", second.Replay.Body)
	}

	// Same key, different body hash: the key was reused.
	third := &Ctx{
		RequestID: "idem-third-" + randomID(t),
		Header:    header,
		RawBody:   []byte(`{"model":"other","messages":[]}`),
		Auth:      &AuthContext{UserID: 7, KeyID: apiKeyID},
	}
	if err := stage.Run(t.Context(), third); err != ErrPipelineStop {
		t.Fatalf("reuse err = %v, want ErrPipelineStop", err)
	}
	if third.Replay == nil || third.Replay.Code != "idempotency_key_reused" {
		t.Fatalf("reuse decision = %+v", third.Replay)
	}
}

// TestIntegrationPreholdHonoursHoldCap drives the real ledger transaction.
func TestIntegrationPreholdHonoursHoldCap(t *testing.T) {
	userID := seedAccount(t, 6500)
	ledgerStore := ledger.NewStore(testDB, nil)

	deps := staticDeps(nil)
	deps.Ledger = ledgerStore
	deps.NodeID = "gw-test"
	deps.Config = nil

	stage := NewPreholdStage(deps)
	est := billingPreestimateForTest()
	c := &Ctx{
		RequestID: "hold-" + randomID(t),
		Auth:      &AuthContext{UserID: userID, KeyID: 1},
		Model:     &ModelEntry{ID: 1},
		Est:       &est,
	}
	// raw_est 6000 <= 6500, est 7200, hold = min(7200, 6500×50%) = 3250.
	c.Est.RawMicro = 6000
	c.Est.EstMicro = 7200
	c.RawEstMicro = 6000
	if err := stage.Run(t.Context(), c); err != nil {
		t.Fatalf("prehold: %v", err)
	}
	if c.Hold == nil || c.HoldMicro != 3250 {
		t.Fatalf("hold = %+v, want 3250", c.Hold)
	}
	var state string
	if err := testDB.QueryRowContext(t.Context(),
		`SELECT state FROM t_prehold_hold WHERE request_id = ?`, c.RequestID).Scan(&state); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	if state != ledger.HoldStateHeld {
		t.Fatalf("state = %q, want HELD", state)
	}

	// A balance below raw_est must be 402 even though est would fit the cap.
	poorUser := seedAccount(t, 5000)
	poorEst := billingPreestimateForTest()
	poor := &Ctx{
		RequestID: "hold-poor-" + randomID(t),
		Auth:      &AuthContext{UserID: poorUser, KeyID: 1},
		Model:     &ModelEntry{ID: 1},
		Est:       &poorEst,
	}
	poor.Est.RawMicro = 6000
	poor.Est.EstMicro = 7200
	err := stage.Run(t.Context(), poor)
	assertAPIError(t, err, apierr.CodeInsufficientCredits, http.StatusPaymentRequired)
}

func TestIntegrationStartupCheckPassesOnSeededConfig(t *testing.T) {
	if err := config.StartupCheck(t.Context(), testDB); err != nil {
		t.Fatalf("startup check: %v", err)
	}
}

// billingPreestimateForTest returns a small estimate with the token shape the
// rate limiter and prehold need.
func billingPreestimateForTest() billing.Preestimate {
	return billing.Preestimate{
		Tokens: billing.TokenEstimate{
			InputTokens: 100, OutputTokens: 100, SafeInputTokens: 110, SafeOutputTokens: 110, SafetyBP: 11000,
		},
		RawMicro: 0, EstMicro: 0,
	}
}

func errAs(err error, target any) bool {
	return errors.As(err, target)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestRoutingSnapshotDecodesFromRedis proves the cfg:snap:ROUTING decoder runs
// against a payload written through the real T05 loader, including the P06
// whitelist and the four-dimension policy.
func TestRoutingSnapshotDecodesFromRedis(t *testing.T) {
	store := writeRouting(t, map[string]any{
		"models": []map[string]any{{
			"id": 1, "model_name": "snap-model", "region": "DOMESTIC",
			"model_type": "TEXT", "listing_state": "LISTED", "context_length": 4096,
		}},
		"routes": []map[string]any{{
			"id": 1, "model_id": 1, "channel_id": 1, "upstream_model_name": "up",
			"region": "DOMESTIC", "weight": 10, "priority": 100,
			"stream_supported": true, "enabled": 1,
		}},
		"channels": []map[string]any{{
			"id": 1, "region": "DOMESTIC", "enabled": true,
			"group_scope_mode": "ALL", "stream_supported": 1,
		}},
		"groups": map[string]any{
			"standard": map[string]any{"code": "standard", "ratio_bp": 10000,
				"model_scope_mode": "ALL", "channel_scope_mode": "ALL"},
		},
		"keyModels": map[string]any{"10": map[string]any{"mode": "ALL"}},
		"ipRules": map[string]any{
			"10": map[string]any{"version": 3, "cidrs": []string{"10.1.0.0/16"}},
		},
		"rateLimits": map[string]any{
			"KEY:10": map[string]any{"rpm": 120, "tpm": 100000, "concurrency": 7},
			"USER:7": map[string]any{"daily_quota_micro": 5000},
		},
	})

	catalog := NewCatalog(store)
	snap, err := catalog.Current()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	model := snap.ModelByName("snap-model")
	if model == nil || model.ID != 1 {
		t.Fatalf("model = %+v", model)
	}
	if routes := snap.RoutesFor(1); len(routes) != 1 || routes[0].ChannelID != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	if ch, ok := snap.Channel(1); !ok || !ch.Enabled.Bool() {
		t.Fatalf("channel = %+v, %v", ch, ok)
	}
	limits, ok := snap.RateLimitFor(10, 7)
	if !ok || limits.RPM != 120 || limits.Concurrency != 7 ||
		limits.DailyQuotaMicro != 5000 || !limits.DailyEnabled {
		t.Fatalf("limits = %+v, ok=%v", limits, ok)
	}

	deps := staticDeps(catalog)
	allowed := &Ctx{
		ClientIP: netip.MustParseAddr("10.1.2.3"),
		Auth:     &AuthContext{KeyID: 10, IPMRuleVer: 3},
	}
	if err := NewIPStage(deps).Run(t.Context(), allowed); err != nil {
		t.Fatalf("whitelisted IP rejected: %v", err)
	}
	denied := &Ctx{
		ClientIP: netip.MustParseAddr("8.8.8.8"),
		Auth:     &AuthContext{KeyID: 10, IPMRuleVer: 3},
	}
	assertAPIError(t, NewIPStage(deps).Run(t.Context(), denied),
		apierr.CodeIPNotAllowed, http.StatusForbidden)

	stale := &Ctx{
		ClientIP: netip.MustParseAddr("10.1.2.3"),
		Auth:     &AuthContext{KeyID: 10, IPMRuleVer: 2},
	}
	assertAPIError(t, NewIPStage(deps).Run(t.Context(), stale),
		apierr.CodeIPNotAllowed, http.StatusForbidden)
}
