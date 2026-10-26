package user

import (
	"testing"
	"time"
)

// A11: a 5,000-credit daily limit, 500 credits used and a 500-credit/hour burn
// predicts 9 more hours (the card's "about 10 h ±10%"). The arithmetic is
// (limit-used)/burn; using limit/burn would double-count the used part.
func TestComputeETA(t *testing.T) {
	now := time.Date(2026, 10, 27, 1, 2, 3, 0, time.UTC)
	limit := int64(50_000_000) // 5,000 credits
	got := computeETA(now, &limit, 5_000_000, 5_000_000)
	if !got.Reachable || got.Reason != "OK" {
		t.Fatalf("eta = %+v", got)
	}
	if got.SecondsLeft == nil {
		t.Fatal("seconds_left is nil")
	}
	const want = int64(9 * 3600)
	if *got.SecondsLeft != want {
		t.Fatalf("seconds_left = %d, want %d", *got.SecondsLeft, want)
	}
	if got.ETAAt == nil || !got.ETAAt.Equal(now.Add(time.Duration(*got.SecondsLeft)*time.Second)) {
		t.Fatalf("eta_at = %v", got.ETAAt)
	}
	// The card allows ±10% around 10 hours.
	if *got.SecondsLeft < 9*3600 || *got.SecondsLeft > 11*3600 {
		t.Fatalf("seconds_left = %d outside the 9–11 h window", *got.SecondsLeft)
	}
}

func TestComputeETANoDailyQuota(t *testing.T) {
	got := computeETA(time.Now(), nil, 0, 1_000_000)
	if got.Reachable {
		t.Fatal("FREE_DEFAULT with no daily quota must not predict a ceiling")
	}
	if got.Reason != "NO_DAILY_QUOTA" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if got.ETAAt != nil || got.SecondsLeft != nil {
		t.Fatalf("eta fields must be null: %+v", got)
	}
}

func TestComputeETAQuotaAlreadyReached(t *testing.T) {
	limit := int64(10_000)
	got := computeETA(time.Now(), &limit, 10_000, 0)
	if !got.Reachable || got.Reason != "QUOTA_REACHED" {
		t.Fatalf("eta = %+v", got)
	}
	if got.SecondsLeft == nil || *got.SecondsLeft != 0 {
		t.Fatalf("seconds_left = %v, want 0", got.SecondsLeft)
	}
}

func TestComputeETANoRecentUsage(t *testing.T) {
	limit := int64(10_000)
	got := computeETA(time.Now(), &limit, 1_000, 0)
	if got.Reachable || got.Reason != "NO_RECENT_USAGE" {
		t.Fatalf("eta = %+v", got)
	}
}
