package fund

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A7 / G-09: a second claim with the same device fingerprint is refused, and
// so are the phone and IP-segment variants. The trial is granted exactly once.
func TestA7TrialAntiAbuse(t *testing.T) {
	svc, _ := newTestService(t)

	u1 := newTestUser(t, nil)
	u2 := newTestUser(t, nil)
	u3 := newTestUser(t, nil)
	u4 := newTestUser(t, nil)
	setUserPhone(t, u1, "8613800138000")
	setUserPhone(t, u3, "8613800138000")

	rec := callWithRemote(t, svc.ClaimTrial, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": "device-alpha"}, u1, "203.0.113.10:4111", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first claim status=%d body=%s", rec.Code, rec.Body.String())
	}
	var granted trialClaimResponse
	decodeBody(t, rec, &granted)
	if granted.GrantedMicro != 70_000_000 || granted.GrantedCredits != 7000 {
		t.Fatalf("granted = %d micro (%d credits), want 70,000,000 (7000)",
			granted.GrantedMicro, granted.GrantedCredits)
	}
	b := balanceOf(t, u1)
	if b.Bonus != 70_000_000 || b.Total != 70_000_000 {
		t.Fatalf("balance after trial = bonus %d total %d, want 70,000,000", b.Bonus, b.Total)
	}
	var claimed bool
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT trial_claimed_at IS NOT NULL FROM t_user WHERE id = ?`, u1).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("trial_claimed_at is NULL after a successful claim")
	}
	var trialGranted int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_device_fingerprint WHERE fp_hash = ? AND trial_granted = 1`,
		fingerprintHash(svc.FingerprintKey, "device-alpha")).Scan(&trialGranted); err != nil {
		t.Fatal(err)
	}
	if trialGranted != 1 {
		t.Fatalf("granted fingerprint rows = %d, want 1", trialGranted)
	}
	t.Logf("first claim: granted=%d credits plan=%s expires=%s",
		granted.GrantedCredits, granted.PlanCode, granted.TrialExpireAt)

	// Same fingerprint, different account and IP.
	assertTrialClaimed := func(label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s status=%d body=%s", label, rec.Code, rec.Body.String())
		}
		eb := errorBody(t, rec)
		if eb.Error.Code != "trial_already_claimed" {
			t.Fatalf("%s code=%q, want trial_already_claimed", label, eb.Error.Code)
		}
		// The four factors share one message on purpose (no probing which one
		// matched).
		if !strings.Contains(eb.Error.Message, "该账号或设备已领取过体验额度") {
			t.Fatalf("%s message=%q", label, eb.Error.Message)
		}
		t.Logf("%s refusal: HTTP %d code=%s", label, rec.Code, eb.Error.Code)
	}

	rec = callWithRemote(t, svc.ClaimTrial, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": "device-alpha"}, u2, "198.51.100.20:4111", nil)
	assertTrialClaimed("same fingerprint", rec)

	// Different fingerprint, same phone.
	rec = callWithRemote(t, svc.ClaimTrial, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": "device-beta"}, u3, "192.0.2.30:4111", nil)
	assertTrialClaimed("same phone", rec)

	// Different fingerprint and phone, but the same /24 as the first claim.
	rec = callWithRemote(t, svc.ClaimTrial, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": "device-gamma"}, u4, "203.0.113.99:4111", nil)
	assertTrialClaimed("same /24", rec)

	// The first account cannot claim twice either.
	rec = callWithRemote(t, svc.ClaimTrial, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": "device-delta"}, u1, "203.0.113.10:4111", nil)
	assertTrialClaimed("second claim by same user", rec)
	if n := countNotifications(t, u1, eventRefundArrived); n != 0 {
		t.Fatalf("trial claim wrote %d refund notifications, want 0", n)
	}
}

// An empty HMAC key is a configuration refusal, never an empty-key hash.
func TestTrialClaimRequiresFingerprintKey(t *testing.T) {
	svc, _ := newTestService(t)
	svc.FingerprintKey = ""
	uid := newTestUser(t, nil)
	rec := callWithRemote(t, svc.ClaimTrial, http.MethodPost, "/api/console/trial/claim",
		map[string]any{"fingerprint": "device-alpha"}, uid, "203.0.113.10:4111", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
	if code := errorBody(t, rec).Error.Code; code != "config_unavailable" {
		t.Fatalf("code=%q, want config_unavailable", code)
	}
}
