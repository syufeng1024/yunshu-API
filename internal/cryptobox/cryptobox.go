// Package cryptobox seals and opens channel upstream keys with AES-256-GCM.
//
// Envelope format: "v1:" + base64.StdEncoding(nonce || ciphertext || tag).
// The KEK comes from CHANNEL_KEY_KEK (base64 of exactly 32 bytes). There is no
// plaintext fallback anywhere: a missing KEK or a malformed envelope is an
// error the caller must turn into 503 config_unavailable.
//
// The plaintext exists only in the caller's local variable between Open and the
// HTTP request write. It must never be put in a Ctx, a log line, an attempt
// record, an error body or a metric label.
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnvKEK is the environment variable holding the base64 KEK.
const EnvKEK = "CHANNEL_KEY_KEK"

// EnvelopePrefix versions the envelope. A future format must use v2:, so an
// old node never silently misreads a new envelope.
const EnvelopePrefix = "v1:"

// keyVersionAAD binds the envelope prefix to the ciphertext.
var keyVersionAAD = []byte("yunshu:channel-key:v1")

// Errors are deliberately value-free: they never contain the KEK, the
// envelope or the plaintext.
var (
	ErrNoKEK       = errors.New("cryptobox: CHANNEL_KEY_KEK is not set")
	ErrInvalidKEK  = errors.New("cryptobox: KEK must be 32 bytes of base64")
	ErrBadEnvelope = errors.New("cryptobox: envelope is not v1:base64")
	ErrDecrypt     = errors.New("cryptobox: envelope authentication failed")
)

// Box holds the AEAD built from one KEK.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from raw KEK bytes. The length must be 32 (AES-256).
func New(kek []byte) (*Box, error) {
	if len(kek) != 32 {
		return nil, ErrInvalidKEK
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		// aes.NewCipher only fails on a wrong key length, already checked.
		return nil, ErrInvalidKEK
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptobox: init GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// NewFromBase64 decodes a base64 KEK and builds the Box.
func NewFromBase64(encoded string) (*Box, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, ErrNoKEK
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrInvalidKEK
	}
	return New(raw)
}

// FromEnv reads CHANNEL_KEY_KEK and builds the Box.
func FromEnv() (*Box, error) {
	return NewFromBase64(os.Getenv(EnvKEK))
}

// Seal encrypts plaintext and returns the "v1:" envelope string.
func (b *Box) Seal(plaintext []byte) (string, error) {
	if b == nil || b.aead == nil {
		return "", ErrNoKEK
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("cryptobox: nonce: %w", err)
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+b.aead.Overhead())
	out = append(out, nonce...)
	out = b.aead.Seal(out, nonce, plaintext, keyVersionAAD)
	return EnvelopePrefix + base64.StdEncoding.EncodeToString(out), nil
}

// SealString is Seal for a string plaintext.
func (b *Box) SealString(plaintext string) (string, error) {
	return b.Seal([]byte(plaintext))
}

// Open decrypts a "v1:" envelope. The plaintext is returned to the caller and
// is never retained by the Box.
func (b *Box) Open(envelope string) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, ErrNoKEK
	}
	envelope = strings.TrimSpace(envelope)
	if !strings.HasPrefix(envelope, EnvelopePrefix) {
		return nil, ErrBadEnvelope
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(envelope, EnvelopePrefix))
	if err != nil {
		return nil, ErrBadEnvelope
	}
	nonceSize := b.aead.NonceSize()
	if len(raw) < nonceSize+b.aead.Overhead() {
		return nil, ErrBadEnvelope
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, keyVersionAAD)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
