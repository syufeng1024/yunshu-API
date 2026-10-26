package apierr

import (
	"strings"
	"time"

	"yunshu.dev/core/internal/ratelimit"
)

// FromVerdict builds the 429 body from an L1 admission verdict. The outward
// code is the hit dimension that recovers last (PRD §3.3); limit/remaining/
// reset_at describe that same dimension only, and every other saturated
// dimension is appended to message as "同时触顶：…".
//
// Headers: the Verdict only carries the main dimension, so a partial header
// snapshot is attached when the main dimension is RPM or daily. T07 should
// call WithRateLimitSnapshot with the full RPM + daily values to satisfy the
// five-header contract even when the main dimension is concurrency or TPM.
func FromVerdict(v ratelimit.Verdict) *APIError {
	return FromVerdictAt(v, time.Now())
}

// FromVerdictAt is FromVerdict with an injected clock, used by tests so the
// retry_after_seconds / reset_at pair is exactly deterministic.
func FromVerdictAt(v ratelimit.Verdict, now time.Time) *APIError {
	code := v.OutwardCode()
	if code == "" {
		// An allowed verdict must not be turned into an error. Treat a
		// malformed rejection as RPM rather than emitting an empty code.
		code = ratelimit.CodeRPM
	}

	retry, resetAt := rateLimitRecovery(code, v, now)
	limit := float64(v.Limit)
	remaining := float64(v.Remaining)

	var e *APIError
	switch code {
	case ratelimit.CodeDailyQuota:
		e = Newf(CodeDailyQuotaExceeded, v.Total, v.Limit)
	case ratelimit.CodeRPM:
		e = Newf(CodeRPMLimitExceeded, v.Limit, retry)
	case ratelimit.CodeTPM:
		e = Newf(CodeTPMLimitExceeded, v.Limit, retry)
	case ratelimit.CodeConcurrency:
		e = Newf(CodeConcurrencyLimitExceeded, v.Limit, retry)
	default:
		// account_rpm_exceeded is registered but has no V1 producer.
		e = Newf(CodeAccountRPMExceeded, v.Limit, retry)
	}
	e.WithRetryAfter(retry)
	e.Limit = &limit
	e.Remaining = &remaining
	e.setResetAt(resetAt)

	if others := otherHits(v.AllHit, code); len(others) > 0 {
		e.Message += "；同时触顶：" + strings.Join(others, "、")
	}

	// Partial snapshot: only fill the dimension the verdict actually
	// describes. Writing the other dimension's numbers here would be a lie.
	snap := RateLimitSnapshot{}
	switch code {
	case ratelimit.CodeRPM:
		snap.RPMLimit = v.Limit
		snap.RPMRemaining = v.Remaining
		snap.RPMResetUnix = resetAt.Unix()
	case ratelimit.CodeDailyQuota:
		snap.DailyLimit = v.Limit
		snap.DailyRemaining = v.Remaining
	}
	e.rateLimit = &snap
	return e
}

// rateLimitRecovery computes retry_after_seconds and reset_at as the same
// instant. RPM/TPM reset at the next minute (1..60), daily at next 00:00
// UTC+8 (1..86400), concurrency is fixed at 3 seconds (PRD §3.3).
func rateLimitRecovery(code string, v ratelimit.Verdict, now time.Time) (int, time.Time) {
	switch code {
	case ratelimit.CodeConcurrency:
		return 3, now.Add(3 * time.Second)
	case ratelimit.CodeDailyQuota:
		return fromReset(v.ResetAt, now, 86400, 1, 86400)
	case ratelimit.CodeRPM, ratelimit.CodeTPM, CodeAccountRPMExceeded:
		return fromReset(v.ResetAt, now, 60, 1, 60)
	default:
		return fromReset(v.ResetAt, now, 60, 1, 60)
	}
}

func fromReset(resetUnix int64, now time.Time, fallback, min, max int) (int, time.Time) {
	if resetUnix <= now.Unix() {
		return fallback, now.Add(time.Duration(fallback) * time.Second)
	}
	secs := resetUnix - now.Unix()
	resetAt := time.Unix(resetUnix, 0)
	// Clamping must move reset_at with the seconds, otherwise the two fields
	// name different instants and the client has to guess which one is true.
	if secs < int64(min) {
		secs = int64(min)
		resetAt = now.Add(time.Duration(min) * time.Second)
	}
	if secs > int64(max) {
		secs = int64(max)
		resetAt = now.Add(time.Duration(max) * time.Second)
	}
	return int(secs), resetAt
}

// otherHits renders every saturated dimension except the main one, in the
// limiter's declaration order. E.6: only message may name them.
func otherHits(all []string, main string) []string {
	labels := make([]string, 0, len(all))
	seen := make(map[string]bool, len(all))
	for _, code := range all {
		if code == main || seen[code] {
			continue
		}
		seen[code] = true
		labels = append(labels, dimensionLabel(code))
	}
	return labels
}

func dimensionLabel(code string) string {
	switch code {
	case ratelimit.CodeConcurrency:
		return "并发"
	case ratelimit.CodeRPM:
		return "RPM"
	case ratelimit.CodeTPM:
		return "TPM"
	case ratelimit.CodeDailyQuota:
		return "日额度"
	default:
		return code
	}
}
