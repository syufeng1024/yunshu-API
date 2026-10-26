package dispatch

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
)

// CooldownSetter is the K23 write surface. It is optional: without it the
// outward 503 is still complete, only the channel-level cooldown is skipped.
type CooldownSetter interface {
	SetCooldown(ctx context.Context, channelID int64, retryAfter time.Duration) (time.Duration, error)
}

// DefaultCapacityRetryAfter is the outward wait when the upstream 429 carries
// no usable Retry-After (PRD E.3 / T08 hard constraint 3).
const DefaultCapacityRetryAfter = 30

// MaxCapacityRetryAfter bounds an upstream-supplied wait (TDS E-06: 60s).
const MaxCapacityRetryAfter = 60

// capacityError maps an upstream 429 to the outward 503. It never returns a
// client 429: upstream capacity is a platform-side problem, while a client 429
// tells the caller to inspect its own quota.
func capacityError(header http.Header) *apierr.APIError {
	seconds := parseRetryAfter(header.Get("Retry-After"))
	return apierr.New(apierr.CodeUpstreamCapacityExceeded).WithRetryAfter(seconds)
}

// parseRetryAfter accepts delta-seconds and HTTP-date forms. Missing or
// malformed values become the 30s default; the result is clamped to 1..60.
func parseRetryAfter(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultCapacityRetryAfter
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return clampRetryAfter(n)
	}
	if when, err := http.ParseTime(raw); err == nil {
		seconds := int(time.Until(when).Seconds())
		if seconds <= 0 {
			return 1
		}
		return clampRetryAfter(seconds)
	}
	return DefaultCapacityRetryAfter
}

// cooldownDuration is the K23 TTL: the upstream Retry-After when present,
// otherwise 5s (TDS E-06), capped at 60s. It is deliberately separate from the
// outward 30s default.
func cooldownDuration(header http.Header) time.Duration {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 5 * time.Second
	}
	return time.Duration(clampRetryAfter(parseRetryAfter(raw))) * time.Second
}

func clampRetryAfter(n int) int {
	if n < 1 {
		return 1
	}
	if n > MaxCapacityRetryAfter {
		return MaxCapacityRetryAfter
	}
	return n
}
