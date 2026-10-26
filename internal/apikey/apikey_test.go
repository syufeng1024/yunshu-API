package apikey

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateShape(t *testing.T) {
	key, hash, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(key, Prefix) {
		t.Fatalf("key %q lacks %q", key, Prefix)
	}
	if got, want := len(key), len(Prefix)+bodyLen; got != want {
		t.Fatalf("key length = %d, want %d", got, want)
	}
	for _, ch := range key[len(Prefix):] {
		if !strings.ContainsRune(crockford, ch) {
			t.Fatalf("key contains %q outside the Crockford alphabet", ch)
		}
	}
	sum := sha256.Sum256([]byte(key))
	if want := hex.EncodeToString(sum[:]); hash != want {
		t.Fatalf("hash = %q, want %q", hash, want)
	}
	prefix, err := HashPrefix16(hash)
	if err != nil {
		t.Fatalf("HashPrefix16: %v", err)
	}
	if len(prefix) != 16 {
		t.Fatalf("HashPrefix16 length = %d", len(prefix))
	}
	if DisplayPrefix(key) != key[:8] || Last4(key) != key[len(key)-4:] {
		t.Fatal("display helpers disagree with slicing")
	}
}

func TestGenerateIsRandom(t *testing.T) {
	a, _, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two generated keys are identical")
	}
}

func TestHashPrefix16RejectsMalformed(t *testing.T) {
	if _, err := HashPrefix16("abc"); err == nil {
		t.Fatal("short digest accepted")
	}
	if _, err := HashPrefix16(strings.Repeat("G", 64)); err == nil {
		t.Fatal("non-hex digest accepted")
	}
}
