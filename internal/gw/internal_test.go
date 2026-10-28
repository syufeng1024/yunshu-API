package gw

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/jsonx"
)

var internalTestSecret = []byte(strings.Repeat("k", InternalMinSecretBytes))

func internalTestBody() []byte {
	return []byte(`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`)
}

func internalTestClaims() InternalClaims {
	return InternalClaims{
		UserID:       7,
		KeyID:        10,
		KeyName:      "console-key",
		GroupCode:    "standard",
		GroupRatioBP: 10000,
		IPMRuleVer:   0,
		KeyExpiryMS:  0,
	}
}

// forgeInternalHeaders signs without Validate, so the tests can present a
// MAC-valid but semantically forbidden claim (KeyID 0/-1, ratio 0) and prove
// the receiver rejects it rather than trusting the signer blindly.
func forgeInternalHeaders(h http.Header, secret []byte, ts int64, body []byte, c InternalClaims) {
	sig := base64.StdEncoding.EncodeToString(
		internalMAC(secret, internalCanonical(ts, BodyHashOf(body), c)))
	h.Set(InternalSigHeader, InternalSigVersion+":"+strconv.FormatInt(ts, 10)+":"+sig)
	h.Set(InternalUserIDHeader, strconv.FormatInt(c.UserID, 10))
	h.Set(InternalKeyIDHeader, strconv.FormatInt(c.KeyID, 10))
	h.Set(InternalKeyNameHeader, base64.RawURLEncoding.EncodeToString([]byte(c.KeyName)))
	h.Set(InternalGroupHeader, c.GroupCode)
	h.Set(InternalRatioHeader, strconv.FormatInt(c.GroupRatioBP, 10))
	h.Set(InternalIPMHeader, strconv.FormatInt(c.IPMRuleVer, 10))
	h.Set(InternalExpiryHeader, strconv.FormatInt(c.KeyExpiryMS, 10))
}

func newInternalHandler(t *testing.T, catalog *Catalog, terminal Terminal) (*InternalHandler, *Deps) {
	t.Helper()
	deps := serverDeps(catalog, nil)
	h, err := NewInternalHandler(deps, terminal, string(internalTestSecret))
	if err != nil {
		t.Fatalf("NewInternalHandler: %v", err)
	}
	return h, deps
}

// TestInternalSignatureCanonical pins the exact byte string from the card. A
// reordering, an extra separator or a trailing newline fails here before any
// cross-process test can hide it.
func TestInternalSignatureCanonical(t *testing.T) {
	claims := InternalClaims{
		UserID: 7, KeyID: 10, KeyName: "key-a", GroupCode: "standard",
		GroupRatioBP: 10000, IPMRuleVer: 3, KeyExpiryMS: 123,
	}
	want := "v1\n1700000000\nabc\n7\n10\nkey-a\nstandard\n10000\n3\n123"
	if got := internalCanonical(1700000000, "abc", claims); got != want {
		t.Fatalf("canonical =\n%q\nwant\n%q", got, want)
	}
}

func TestInternalSignatureRoundTrip(t *testing.T) {
	// A non-ASCII key name must survive the header transport unchanged; the
	// canonical is built from the decoded bytes.
	claims := internalTestClaims()
	claims.KeyName = "调试 key"
	claims.IPMRuleVer = 42
	claims.KeyExpiryMS = 1_800_000_000_000
	body := internalTestBody()
	ts := int64(1_800_000_000)

	h := http.Header{}
	if err := SetInternalHeaders(h, internalTestSecret, ts, body, claims); err != nil {
		t.Fatalf("SetInternalHeaders: %v", err)
	}
	got, err := VerifyInternal(internalTestSecret, ts, body, h)
	if err != nil {
		t.Fatalf("VerifyInternal: %v", err)
	}
	if got != claims {
		t.Fatalf("claims = %+v, want %+v", got, claims)
	}
}

// TestInternalSignatureWindow: ±60s inclusive, 61s rejected in both
// directions.
func TestInternalSignatureWindow(t *testing.T) {
	claims := internalTestClaims()
	body := internalTestBody()
	now := int64(1_800_000_000)

	build := func(ts int64) http.Header {
		h := http.Header{}
		if err := SetInternalHeaders(h, internalTestSecret, ts, body, claims); err != nil {
			t.Fatalf("SetInternalHeaders: %v", err)
		}
		return h
	}

	if _, err := VerifyInternal(internalTestSecret, now+InternalSigWindowSeconds, body, build(now)); err != nil {
		t.Fatalf("60s old signature rejected: %v", err)
	}
	if _, err := VerifyInternal(internalTestSecret, now-InternalSigWindowSeconds, body, build(now)); err != nil {
		t.Fatalf("60s future signature rejected: %v", err)
	}
	if _, err := VerifyInternal(internalTestSecret, now+InternalSigWindowSeconds+1, body, build(now)); err == nil {
		t.Fatal("61s old signature accepted")
	}
	if _, err := VerifyInternal(internalTestSecret, now-InternalSigWindowSeconds-1, body, build(now)); err == nil {
		t.Fatal("61s future signature accepted")
	}
}

// TestInternalSignatureBodyBinding: one byte of body drift invalidates the MAC.
func TestInternalSignatureBodyBinding(t *testing.T) {
	claims := internalTestClaims()
	body := internalTestBody()
	ts := time.Now().UTC().Unix()
	h := http.Header{}
	if err := SetInternalHeaders(h, internalTestSecret, ts, body, claims); err != nil {
		t.Fatalf("SetInternalHeaders: %v", err)
	}
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = 'j' // hi -> hj
	if _, err := VerifyInternal(internalTestSecret, ts, tampered, h); err == nil {
		t.Fatal("tampered body accepted")
	}
}

// TestInternalHandlerHappyPath drives P06..P14 with a real signed request and
// proves the injected KeyID reaches P10 (the ledger write) unchanged.
func TestInternalHandlerHappyPath(t *testing.T) {
	var seen *AuthContext
	terminal := TerminalFunc(func(w http.ResponseWriter, _ *http.Request, c *Ctx) {
		cp := *c.Auth
		seen = &cp
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"terminal":true}`))
	})
	h, deps := newInternalHandler(t, basicCatalog(), terminal)

	body := internalTestBody()
	claims := internalTestClaims()
	req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
	req.Header.Set("X-Request-Id", "rid-internal-ok")
	if err := SetInternalHeaders(req.Header, internalTestSecret, time.Now().UTC().Unix(), body, claims); err != nil {
		t.Fatalf("SetInternalHeaders: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if seen == nil {
		t.Fatal("terminal was not reached")
	}
	if seen.KeyID != 10 || seen.UserID != 7 || seen.GroupCode != "standard" {
		t.Fatalf("injected Auth = %+v", *seen)
	}
	if deps.Limiter.(*fakeLimiter).acquireCount() != 1 {
		t.Fatal("P11 was not reached exactly once")
	}
	if deps.Ledger.(*fakeLedger).preholdCount() != 1 {
		t.Fatal("P10 was not reached exactly once")
	}
	if got := deps.Ledger.(*fakeLedger).preholds[0].APIKeyID; got != 10 {
		t.Fatalf("prehold api_key_id = %d, want 10", got)
	}
	if got := deps.Ledger.(*fakeLedger).preholds[0].UserID; got != 7 {
		t.Fatalf("prehold user_id = %d, want 7", got)
	}
	if got := rec.Header().Get("X-Request-Id"); got != "rid-internal-ok" {
		t.Fatalf("X-Request-Id = %q", got)
	}
}

// TestInternalHandlerRejectsBadSignature covers the four wire-visible failure
// modes. Every one must produce the same 401 invalid_api_key body.
func TestInternalHandlerRejectsBadSignature(t *testing.T) {
	claims := internalTestClaims()
	body := internalTestBody()
	now := time.Now().UTC().Unix()

	cases := []struct {
		name  string
		build func() (*http.Request, []byte)
	}{
		{
			name: "wrong secret",
			build: func() (*http.Request, []byte) {
				req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
				other := []byte(strings.Repeat("z", InternalMinSecretBytes))
				if err := SetInternalHeaders(req.Header, other, now, body, claims); err != nil {
					t.Fatalf("SetInternalHeaders: %v", err)
				}
				return req, body
			},
		},
		{
			name: "timestamp 61s old",
			build: func() (*http.Request, []byte) {
				req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
				if err := SetInternalHeaders(req.Header, internalTestSecret, now-61, body, claims); err != nil {
					t.Fatalf("SetInternalHeaders: %v", err)
				}
				return req, body
			},
		},
		{
			name: "tampered body",
			build: func() (*http.Request, []byte) {
				// Keep the JSON valid so P04 accepts it: the request must die at
				// the signature check, not at body parsing.
				tampered := bytes.ReplaceAll(body, []byte(`"hi"`), []byte(`"HI"`))
				req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(tampered))
				if err := SetInternalHeaders(req.Header, internalTestSecret, now, body, claims); err != nil {
					t.Fatalf("SetInternalHeaders: %v", err)
				}
				return req, tampered
			},
		},
		{
			name: "no signature",
			build: func() (*http.Request, []byte) {
				return httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body)), body
			},
		},
		{
			name: "short secret",
			build: func() (*http.Request, []byte) {
				req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
				if err := SetInternalHeaders(req.Header, []byte("short"), now, body, claims); err != nil {
					// Signing with a short secret is refused locally; forge a
					// syntactically valid header so the receiver's own length
					// check is what rejects it.
					req.Header.Set(InternalSigHeader, "v1:"+strconv.FormatInt(now, 10)+":"+
						base64.StdEncoding.EncodeToString([]byte("not-a-mac")))
				}
				return req, body
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, deps := newInternalHandler(t, basicCatalog(), noopTerminal)
			req, _ := tc.build()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			var decoded map[string]any
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if code := errorBody(t, decoded)["code"]; code != "invalid_api_key" {
				t.Fatalf("code = %v, want invalid_api_key", code)
			}
			if deps.Limiter.(*fakeLimiter).acquireCount() != 0 {
				t.Fatal("a rejected signature reached P11")
			}
			if deps.Ledger.(*fakeLedger).preholdCount() != 0 {
				t.Fatal("a rejected signature reached P10")
			}
		})
	}
}

// TestInternalHandlerRejectsSentinelKeyID: even a MAC-valid claim is refused
// when KeyID is 0/-1 or the ratio would make calls free.
func TestInternalHandlerRejectsSentinelKeyID(t *testing.T) {
	body := internalTestBody()
	now := time.Now().UTC().Unix()

	cases := []struct {
		name   string
		mutate func(*InternalClaims)
	}{
		{name: "key_id 0", mutate: func(c *InternalClaims) { c.KeyID = 0 }},
		{name: "key_id -1", mutate: func(c *InternalClaims) { c.KeyID = -1 }},
		{name: "user_id 0", mutate: func(c *InternalClaims) { c.UserID = 0 }},
		{name: "ratio 0", mutate: func(c *InternalClaims) { c.GroupRatioBP = 0 }},
		{name: "ratio above 10000", mutate: func(c *InternalClaims) { c.GroupRatioBP = 10001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, deps := newInternalHandler(t, basicCatalog(), noopTerminal)
			claims := internalTestClaims()
			tc.mutate(&claims)

			req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
			forgeInternalHeaders(req.Header, internalTestSecret, now, body, claims)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if deps.Ledger.(*fakeLedger).preholdCount() != 0 {
				t.Fatal("sentinel claim reached P10")
			}
		})
	}
}

// TestInternalHandlerExpiredKeyIs401 proves defence in depth: controld checks
// expiry from MySQL, and the gateway refuses a signed-but-expired claim too.
func TestInternalHandlerExpiredKeyIs401(t *testing.T) {
	h, deps := newInternalHandler(t, basicCatalog(), noopTerminal)
	body := internalTestBody()
	claims := internalTestClaims()
	claims.KeyExpiryMS = time.Now().UTC().Add(-time.Minute).UnixMilli()

	req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
	if err := SetInternalHeaders(req.Header, internalTestSecret, time.Now().UTC().Unix(), body, claims); err != nil {
		t.Fatalf("SetInternalHeaders: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if code := errorBody(t, decoded)["code"]; code != "key_expired" {
		t.Fatalf("code = %v, want key_expired", code)
	}
	if deps.Ledger.(*fakeLedger).preholdCount() != 0 {
		t.Fatal("expired key reached P10")
	}
}

// TestInternalPipelineUsesInjectedKeyIDForP06 proves the whitelist lookup uses
// the signed KeyID, not a sentinel: the rule exists only for key 10.
func TestInternalPipelineUsesInjectedKeyIDForP06(t *testing.T) {
	catalog := NewStaticCatalog(&RoutingSnapshot{
		IPRules: map[string]IPRule{
			"10": {Version: 0, CIDRs: []string{"10.0.0.0/8"}},
		},
	})
	h, _ := newInternalHandler(t, catalog, noopTerminal)
	body := internalTestBody()
	claims := internalTestClaims()

	req := httptest.NewRequest(http.MethodPost, InternalPath, bytes.NewReader(body))
	if err := SetInternalHeaders(req.Header, internalTestSecret, time.Now().UTC().Unix(), body, claims); err != nil {
		t.Fatalf("SetInternalHeaders: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if code := errorBody(t, decoded)["code"]; code != "ip_not_allowed" {
		t.Fatalf("code = %v, want ip_not_allowed", code)
	}
}

// TestInternalPipelineWithoutAuthDenies is the "Auth 未注入时不放行" case: the
// internal pipeline must not run P07..P10 on an anonymous Ctx.
func TestInternalPipelineWithoutAuthDenies(t *testing.T) {
	pipeline := NewInternalPipeline(staticDeps(basicCatalog()))
	err := pipeline.Run(context.Background(), &Ctx{
		Endpoint:  EndpointChat,
		Protocol:  ProtocolOpenAI,
		RequestID: "rid-no-auth",
		RawBody:   internalTestBody(),
		BodyHash:  BodyHashOf(internalTestBody()),
		Header:    http.Header{},
	})
	if err == nil {
		t.Fatal("pipeline accepted an unauthenticated Ctx")
	}
	se, ok := err.(*StageError)
	if !ok {
		t.Fatalf("error type = %T, want *StageError", err)
	}
	if se.Stage != "P06" || se.API == nil || se.API.Code != apierr.CodeInternalError {
		t.Fatalf("error = %#v, want P06 internal_error", se)
	}
}
