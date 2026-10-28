package gw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/redisx"
)

// AuthStage performs P05.
type AuthStage struct{ d *Deps }

// NewAuthStage builds P05.
func NewAuthStage(d *Deps) *AuthStage { return &AuthStage{d: d} }

// Name implements Stage.
func (s *AuthStage) Name() string { return "P05" }

// Run implements Stage. It reads K13 exactly once (grp/gp come from the same
// HGETALL) and never touches MySQL: gatewayd has no general primary pool, so a
// cache miss is a 401 by construction (TDS §1, §3.2 P05).
func (s *AuthStage) Run(ctx context.Context, c *Ctx) error {
	// Pre-set identity means the internal entry (T27) already verified the
	// signed claims. Its only legitimate injector is InternalHandler, after
	// VerifyInternal succeeded; the public /v1 path always arrives here with a
	// nil Auth. Returning early is the one behaviour change this card allows.
	if c.Auth != nil {
		return nil
	}

	token, authErr := Credential(c.Header)
	if authErr != nil {
		return StageErr("P05", authErr)
	}

	hash := KeyHash16(token)
	rec, err := s.d.Auth.Lookup(ctx, hash)
	if errors.Is(err, redisx.ErrKeyAuthNotFound) {
		return StageErr("P05", apierr.New(apierr.CodeInvalidAPIKey))
	}
	if err != nil {
		s.warn(ctx, "key_cache_read_failed: "+err.Error())
		return StageErr("P05", apierr.New(apierr.CodeAuthUnavailable))
	}

	if !strings.EqualFold(rec.ST, "ENABLED") {
		return StageErr("P05", apierr.New(apierr.CodeKeyDisabled))
	}
	now := s.nowMS()
	if rec.EXP > 0 && rec.EXP <= now {
		expired := time.UnixMilli(rec.EXP).UTC().Format(time.RFC3339)
		return StageErr("P05", apierr.Newf(apierr.CodeKeyExpired, expired))
	}

	group := rec.GRP
	if group == "" {
		group = "standard"
		s.warn(ctx, "key_group_missing: grp missing in K13, using standard")
	}
	ratio := rec.GP
	if ratio <= 0 {
		ratio = 10000
		s.warn(ctx, "key_group_ratio_missing: gp missing in K13, using 10000")
	}

	limits, ok := Limits{}, false
	if s.d.Catalog != nil {
		if snap, catErr := s.d.Catalog.Current(); catErr == nil {
			limits, ok = snap.RateLimitFor(rec.KID, rec.UID)
		} else {
			return StageErr("P05", apierr.New(apierr.CodeConfigUnavailable).WithCause(catErr))
		}
	}
	if !ok {
		limits = FreeDefaultLimits()
		s.warn(ctx, "rate_limit_policy_missing: using FREE_DEFAULT")
	}

	c.Auth = &AuthContext{
		UserID:       rec.UID,
		KeyID:        rec.KID,
		KeyName:      rec.KN,
		GroupCode:    group,
		GroupRatioBP: ratio,
		IPMRuleVer:   rec.IPM,
		KeyExpiryMS:  rec.EXP,
	}
	c.Limits = limits

	// P01's per-key connection cap. It cannot run at accept time because the
	// key is unknown until the headers are parsed; enforcing it immediately
	// after P05 is the earliest enforceable point.
	if !s.d.ConnGuard.Acquire(ctx, rec.KID, limits.Concurrency) {
		return ErrConnectionReset
	}
	c.ConnHeld = true
	return nil
}

func (s *AuthStage) nowMS() int64 { return s.d.ClockNowMS() }

func (s *AuthStage) warn(ctx context.Context, msg string) {
	if s.d.Logger != nil {
		s.d.Logger.Warn(ctx, msg, obslog.Fields{Stage: "P05"})
	}
}

// Credential extracts the bearer token. Authorization wins over x-api-key;
// when both are present and disagree the request is rejected rather than
// silently choosing one, which would hide a client misconfiguration.
func Credential(h http.Header) (string, *apierr.APIError) {
	authz := strings.TrimSpace(h.Get("Authorization"))
	xkey := strings.TrimSpace(h.Get("x-api-key"))

	bearer, bearerOK := bearerToken(authz)
	switch {
	case authz != "" && xkey != "":
		if bearerOK && bearer == xkey {
			return bearer, nil
		}
		return "", apierr.New(apierr.CodeConflictingAuthHeaders)
	case authz != "":
		if !bearerOK || bearer == "" {
			return "", apierr.New(apierr.CodeInvalidAPIKey)
		}
		return bearer, nil
	case xkey != "":
		return xkey, nil
	default:
		return "", apierr.New(apierr.CodeInvalidAPIKey)
	}
}

func bearerToken(authz string) (string, bool) {
	const prefix = "Bearer "
	if len(authz) < len(prefix) || !strings.EqualFold(authz[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(authz[len(prefix):]), true
}

// KeyHash16 returns the K13 suffix: the first 16 hex characters of the SHA-256
// of the plaintext key.
func KeyHash16(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}
