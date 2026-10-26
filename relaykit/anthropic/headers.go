package anthropic

import (
	"errors"
	"fmt"

	"yunshu.dev/relaykit/ir"
)

// Anthropic requires x-api-key and anthropic-version on every request. The
// version is pinned by the adapter, not by the client: a client-supplied
// version must not reach the upstream (see docs/relay-contract.md).
const (
	HeaderAPIKey   = "x-api-key"
	HeaderVersion  = "anthropic-version"
	HeaderContent  = "content-type"
	DefaultVersion = "2023-06-01"
)

// ErrMaxTokensRequired is returned when a request bound for an Anthropic
// upstream has no max_tokens. Anthropic rejects such a body; emitting it
// anyway turns a local validation problem into a 400 from the upstream.
var ErrMaxTokensRequired = errors.New("anthropic: max_tokens is required")

// Headers returns the authentication/version headers for an Anthropic
// upstream. version defaults to DefaultVersion when empty.
func Headers(apiKey, version string) map[string]string {
	if version == "" {
		version = DefaultVersion
	}
	out := map[string]string{
		HeaderAPIKey:  apiKey,
		HeaderVersion: version,
		HeaderContent: "application/json",
	}
	return out
}

// ValidateRequest enforces the members Anthropic treats as mandatory.
func ValidateRequest(req *ir.Request) error {
	if req == nil {
		return fmt.Errorf("anthropic: nil request")
	}
	if req.MaxTokens == nil || *req.MaxTokens <= 0 {
		return ErrMaxTokensRequired
	}
	return nil
}
