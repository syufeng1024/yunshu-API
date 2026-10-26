package dispatch

import (
	"context"
	"math/rand/v2"
	"time"
)

// MaxAttempts is the hard cap from TDS §3.3.2: one initial try plus at most one
// retry. It is never raised by a channel's retry_max.
const MaxAttempts = 2

// Retryable is the strict set from T08 hard constraint 6: HTTP 5xx and the
// three timeouts. DNS, TLS, empty responses, malformed bodies and every 4xx are
// excluded, including 429.
func Retryable(class string) bool {
	return class == ClassHTTP5XX || class == ClassTimeout
}

// RetryInput is the predicate input. FlushedBytes must stay zero: once a byte
// reached the client, a retry would duplicate content and desynchronise the
// billed token count from what the client saw.
type RetryInput struct {
	Attempts         int
	MaxAttempts      int
	FlushedBytes     int64
	Class            string
	Remaining        time.Duration
	Threshold        time.Duration
	HasNextCandidate bool
}

// DecideRetry returns whether to retry and which label to record.
func DecideRetry(in RetryInput) (bool, string) {
	if in.FlushedBytes > 0 {
		return false, DecisionNoRetry
	}
	if !Retryable(in.Class) {
		return false, DecisionNoRetry
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 || maxAttempts > MaxAttempts {
		maxAttempts = MaxAttempts
	}
	if in.Attempts >= maxAttempts {
		return false, DecisionNoRetry
	}
	// A retry must fit entirely inside the remaining request budget. A negative
	// Remaining means "no deadline was installed"; the per-attempt context still
	// bounds the try.
	if in.Remaining > 0 && in.Threshold > 0 && in.Remaining < in.Threshold {
		return false, DecisionNoRetry
	}
	if in.HasNextCandidate {
		return true, DecisionNextCandidate
	}
	// With no second candidate left, retrying the same route is the only option.
	// It is limited to the retryable set above; 429 and 4xx never get here.
	return true, DecisionSameRoute
}

// backoff sleeps 100ms ± 50ms before a retry. It is ctx-aware and therefore
// never keeps a goroutine alive after a client disconnect or the request
// deadline (PRD E.3).
func backoff(ctx context.Context) bool {
	d := 100*time.Millisecond + time.Duration(rand.IntN(50))*time.Millisecond
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
