package cryptobox

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKEK(t *testing.T) []byte {
	t.Helper()
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	return kek
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(testKEK(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	envelope, err := box.SealString("sk-local-dev-key")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.HasPrefix(envelope, EnvelopePrefix) {
		t.Fatalf("envelope = %q, want v1: prefix", envelope)
	}
	if strings.Contains(envelope, "sk-local-dev-key") {
		t.Fatal("plaintext leaked into the envelope")
	}
	plain, err := box.Open(envelope)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plain) != "sk-local-dev-key" {
		t.Fatalf("plaintext = %q", plain)
	}
}

func TestOpenRejectsTamperAndWrongKey(t *testing.T) {
	box, _ := New(testKEK(t))
	envelope, _ := box.SealString("secret")

	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(envelope, EnvelopePrefix))
	raw[len(raw)-1] ^= 0xff
	tampered := EnvelopePrefix + base64.StdEncoding.EncodeToString(raw)
	if _, err := box.Open(tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered envelope error = %v, want ErrDecrypt", err)
	}

	other := testKEK(t)
	other[0] ^= 0xff
	box2, _ := New(other)
	if _, err := box2.Open(envelope); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key error = %v, want ErrDecrypt", err)
	}
}

func TestOpenRejectsBadEnvelope(t *testing.T) {
	box, _ := New(testKEK(t))
	for _, envelope := range []string{"", "v1:", "v2:abc", "plaintext", "v1:!!!!"} {
		if _, err := box.Open(envelope); err == nil {
			t.Fatalf("envelope %q accepted", envelope)
		}
	}
}

func TestNewRejectsWrongKEKLength(t *testing.T) {
	if _, err := New([]byte("short")); !errors.Is(err, ErrInvalidKEK) {
		t.Fatalf("short KEK error = %v", err)
	}
	if _, err := NewFromBase64(""); !errors.Is(err, ErrNoKEK) {
		t.Fatalf("empty env error = %v", err)
	}
	if _, err := NewFromBase64("not-base64"); !errors.Is(err, ErrInvalidKEK) {
		t.Fatalf("bad base64 error = %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	kek := testKEK(t)
	t.Setenv(EnvKEK, base64.StdEncoding.EncodeToString(kek))
	box, err := FromEnv()
	if err != nil {
		t.Fatalf("from env: %v", err)
	}
	envelope, _ := box.SealString("x")
	if _, err := box.Open(envelope); err != nil {
		t.Fatalf("round trip: %v", err)
	}
}

func TestNilBoxNeverFallsBackToPlaintext(t *testing.T) {
	var box *Box
	if _, err := box.SealString("x"); !errors.Is(err, ErrNoKEK) {
		t.Fatalf("nil seal = %v", err)
	}
	if _, err := box.Open("v1:AAAA"); !errors.Is(err, ErrNoKEK) {
		t.Fatalf("nil open = %v", err)
	}
}
