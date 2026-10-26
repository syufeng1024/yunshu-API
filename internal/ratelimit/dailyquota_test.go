package ratelimit

import (
	"testing"
	"time"
)

func TestDayUsesShanghaiDate(t *testing.T) {
	// 2026-10-26 15:59 UTC is 23:59 in Shanghai; one minute later it is the
	// next calendar day. A UTC formatter would put both in 20261026.
	before := time.Date(2026, 10, 26, 15, 59, 59, 0, time.UTC)
	after := time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)
	if got := Day(before); got != "20261026" {
		t.Fatalf("Day(15:59:59Z) = %q want 20261026", got)
	}
	if got := Day(after); got != "20261027" {
		t.Fatalf("Day(16:00Z) = %q want 20261027", got)
	}
}

func TestNextResetIsCSTMidnight(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) // 20:00 CST
	reset := NextReset(now)
	wantUTC := time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)
	if !reset.Equal(wantUTC) {
		t.Fatalf("NextReset = %v want %v", reset, wantUTC)
	}
	if NextResetUnix(now) != wantUTC.Unix() {
		t.Fatalf("NextResetUnix = %d want %d", NextResetUnix(now), wantUTC.Unix())
	}
}

func TestTTLIncludesGrace(t *testing.T) {
	// 23:59:00 CST on 2026-10-26. Expiry is 00:00 plus the 600s grace, i.e.
	// 00:10, which is 11 minutes from now.
	now := time.Date(2026, 10, 26, 23, 59, 0, 0, time.FixedZone("CST", 8*3600))
	got := TTL(now)
	want := 11 * time.Minute
	if got != want {
		t.Fatalf("TTL = %v want %v", got, want)
	}
	if TTLMillis(now) != int64(want/time.Millisecond) {
		t.Fatalf("TTLMillis mismatch")
	}
}

// T06 hard constraint 9 / errata E-11: the K4/K5 lifetime counts to the next
// CST midnight and then adds the 600s grace, so the key expires at 00:10 --
// not at 00:20, which is what counting to 00:10 and adding 600s produces.
func TestTTLDailyQuotaExpiresAt0010(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 26, 23, 0, 0, 0, cst)
	wantExpiry := time.Date(2026, 10, 27, 0, 10, 0, 0, cst)

	ttl := TTL(now)
	if got := now.Add(ttl); !got.Equal(wantExpiry) {
		t.Fatalf("expiry = %v, want %v (TTL = %v)", got.In(cst), wantExpiry, ttl)
	}
	if ttl != 70*time.Minute {
		t.Fatalf("TTL = %v, want 70m (23:00 -> 00:00 plus 600s grace)", ttl)
	}
	if TTLMillis(now) != int64(70*time.Minute/time.Millisecond) {
		t.Fatalf("TTLMillis = %d, want %d", TTLMillis(now), int64(70*time.Minute/time.Millisecond))
	}
	t.Logf("TTL: now=%s ttl=%s expiry=%s", now.In(cst), ttl, now.Add(ttl).In(cst))
}
