// Package apikey owns the API-key plaintext format and its one-way digest.
//
// The plaintext is shown once, at creation, and never stored. t_api_key keeps
// only SHA-256 (key_hash), the first eight plaintext characters (key_prefix)
// and the last four (key_last4). The key is a 240-bit random Crockford Base32
// string, so a fast digest is sufficient: unlike a password there is no
// dictionary to attack.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Prefix is the literal every key starts with.
const Prefix = "sk-"

// crockford is uppercase Crockford Base32. I, L, O and U are omitted so a
// copied key cannot be misread.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// bodyBytes is 30 random bytes = 240 bits = exactly 48 Base32 characters. The
// TDS says "20 bytes = 160 bit" while also specifying 48 characters; 20 bytes
// only yields 32 characters. The externally visible length wins here and the
// entropy is strictly higher. See the T11 receipt for the recorded erratum.
const bodyBytes = 30

// bodyLen is the number of characters after the "sk-" prefix.
const bodyLen = bodyBytes * 8 / 5

// Generate returns a new plaintext key and its SHA-256 hex digest.
func Generate() (plaintext, hash string, err error) {
	var raw [bodyBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("apikey: random source failed: %w", err)
	}
	plaintext = Prefix + encodeCrockford(raw[:])
	return plaintext, Hash(plaintext), nil
}

// Hash returns the lowercase hex SHA-256 of the complete plaintext.
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// HashPrefix16 returns the K13 suffix: the first 16 hex characters of Hash.
func HashPrefix16(hash string) (string, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 64 {
		return "", fmt.Errorf("apikey: digest must be 64 hex characters, got %d", len(hash))
	}
	for i := 0; i < len(hash); i++ {
		ch := hash[i]
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return "", fmt.Errorf("apikey: digest contains non-lowercase-hex byte %q", ch)
	}
	return hash[:16], nil
}

// DisplayPrefix returns the first eight plaintext characters, which is what
// t_api_key.key_prefix stores.
func DisplayPrefix(plaintext string) string {
	if len(plaintext) < 8 {
		return plaintext
	}
	return plaintext[:8]
}

// Last4 returns the final four plaintext characters.
func Last4(plaintext string) string {
	if len(plaintext) < 4 {
		return plaintext
	}
	return plaintext[len(plaintext)-4:]
}

// encodeCrockford packs src big-endian into 5-bit groups. It is the inverse of
// nothing in this codebase: keys are never decoded.
func encodeCrockford(src []byte) string {
	out := make([]byte, 0, (len(src)*8+4)/5)
	var acc uint64
	var bits uint
	for _, b := range src {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockford[(acc>>bits)&31])
		}
	}
	if bits > 0 {
		out = append(out, crockford[(acc<<(5-bits))&31])
	}
	return string(out)
}
