package apierr

import (
	"net/http"
	"strconv"
)

// RateLimitSnapshot is the per-response rate-limit view (TDS §6.3). The
// limiter's Verdict only describes the main dimension, so a caller that knows
// both dimensions supplies this. A zero Limit means "not known" and the
// corresponding two/three headers are skipped rather than written as a fake
// zero. Remaining may legitimately be zero.
type RateLimitSnapshot struct {
	RPMLimit       int64
	RPMRemaining   int64
	RPMResetUnix   int64
	DailyLimit     int64
	DailyRemaining int64
}

// ResponseMeta is the normal-response header set. Stream decides which credit
// header is legal: stream never gets X-Credits-Consumed (PRD §3.3 / E.4 rule
// 8), even if the caller fills CreditsConsumed by mistake.
type ResponseMeta struct {
	RequestID string

	Stream bool

	CreditsConsumed  *int64
	CreditsPreheld   *int64
	CreditsRemaining *float64

	RateLimit *RateLimitSnapshot
}

// SetResponseHeaders writes the headers for a 200 response and returns the
// request id it used (generating one if the caller passed an empty value).
func SetResponseHeaders(w http.ResponseWriter, m ResponseMeta) string {
	h := w.Header()
	rid := sanitizeRequestID(m.RequestID)
	if rid == "" {
		rid = NewRequestID()
	}
	h.Set("X-Request-Id", rid)

	if m.RateLimit != nil {
		applyRateLimitHeaders(h, *m.RateLimit)
	}
	// A negative remaining on a success response is the "unknown" sentinel
	// from TDS §6.3; never write a fake balance. 402 goes through Write,
	// where a negative remaining is a real debt and is written.
	if m.CreditsRemaining != nil && *m.CreditsRemaining >= 0 {
		h.Set("X-Credits-Remaining", formatNumber(*m.CreditsRemaining))
	}
	if m.Stream {
		if m.CreditsPreheld != nil {
			h.Set("X-Credits-Preheld", strconv.FormatInt(*m.CreditsPreheld, 10))
		}
		// Required so a fronting Nginx does not buffer the whole stream.
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		return rid
	}
	if m.CreditsConsumed != nil {
		h.Set("X-Credits-Consumed", strconv.FormatInt(*m.CreditsConsumed, 10))
	}
	return rid
}

// ApplyRateLimitHeaders writes the five §3.3 headers. Exported for callers
// that assemble a response outside Write (e.g. a replay of a cached 200).
func ApplyRateLimitHeaders(w http.ResponseWriter, s RateLimitSnapshot) {
	applyRateLimitHeaders(w.Header(), s)
}

func applyRateLimitHeaders(h http.Header, s RateLimitSnapshot) {
	if s.RPMLimit > 0 {
		h.Set("X-RateLimit-Limit-Requests", strconv.FormatInt(s.RPMLimit, 10))
		h.Set("X-RateLimit-Remaining-Requests", strconv.FormatInt(s.RPMRemaining, 10))
		h.Set("X-RateLimit-Reset-Requests", strconv.FormatInt(s.RPMResetUnix, 10))
	}
	if s.DailyLimit > 0 {
		h.Set("X-Quota-Daily-Limit", strconv.FormatInt(s.DailyLimit, 10))
		h.Set("X-Quota-Daily-Remaining", strconv.FormatInt(s.DailyRemaining, 10))
	}
}
