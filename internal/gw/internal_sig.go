package gw

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// InternalPath is the only route the internal entry accepts. It is registered
// by cmd/gatewayd on the same listener as /v1/ but is never published: the
// compose stack exposes no extra port and nginx answers 404 for /internal/.
const InternalPath = "/internal/v1/chat/completions"

// Internal header names. The signature header is the only name fixed by the
// T27 contract; the claim headers are this implementation's transport. They
// carry exactly the seven AuthContext fields, so nothing the gateway later
// trusts is unsigned.
const (
	InternalSigHeader     = "X-Yunshu-Internal-Sig"
	InternalUserIDHeader  = "X-Yunshu-Internal-User-Id"
	InternalKeyIDHeader   = "X-Yunshu-Internal-Key-Id"
	InternalKeyNameHeader = "X-Yunshu-Internal-Key-Name"
	InternalGroupHeader   = "X-Yunshu-Internal-Group-Code"
	InternalRatioHeader   = "X-Yunshu-Internal-Group-Ratio-Bp"
	InternalIPMHeader     = "X-Yunshu-Internal-Ip-Rule-Ver"
	InternalExpiryHeader  = "X-Yunshu-Internal-Key-Expiry-Ms"
)

const (
	// InternalSigVersion is the "v1" literal in both the canonical string and
	// the X-Yunshu-Internal-Sig header.
	InternalSigVersion = "v1"
	// InternalSigWindowSeconds is the accepted clock skew, both directions.
	InternalSigWindowSeconds int64 = 60
	// InternalMinSecretBytes is the minimum INTERNAL_CALL_SECRET length. A
	// shorter secret is treated as unconfigured: gatewayd does not register the
	// route and controld answers 503.
	InternalMinSecretBytes = 32
)

// ErrInternalSignature is the single failure returned by VerifyInternal. The
// HTTP layer maps every instance to the same 401 invalid_api_key body, so a
// caller cannot tell a bad MAC from a stale timestamp.
var ErrInternalSignature = errors.New("gw: internal signature rejected")

// InternalClaims is the signed identity the internal entry injects into
// Ctx.Auth. The fields mirror AuthContext one-for-one: KeyID is a real
// t_api_key.id, never a sentinel, because P06/P07/P08/P10/P11 and the
// connection slot all key off it.
type InternalClaims struct {
	UserID       int64
	KeyID        int64
	KeyName      string
	GroupCode    string
	GroupRatioBP int64
	IPMRuleVer   int64
	KeyExpiryMS  int64
}

// Validate rejects claims that could not have come from the control plane and
// that would make a later stage misbehave. It is called both before signing and
// after verification, so a compromised or buggy signer cannot inject a free
// group ratio (0), an empty identity or a canonical-breaking key name.
func (c InternalClaims) Validate() error {
	switch {
	case c.UserID <= 0:
		return fmt.Errorf("%w: user_id", ErrInternalSignature)
	case c.KeyID <= 0:
		return fmt.Errorf("%w: key_id", ErrInternalSignature)
	case c.GroupRatioBP <= 0 || c.GroupRatioBP > 10000:
		// P09 multiplies the price by this ratio. Zero would make every call
		// free; P05 substitutes 10000 for a zero snapshot value, so the signer
		// must do the same instead of forwarding 0.
		return fmt.Errorf("%w: group_ratio_bp", ErrInternalSignature)
	case c.IPMRuleVer < 0:
		return fmt.Errorf("%w: ip_rule_ver", ErrInternalSignature)
	case c.KeyExpiryMS < 0:
		return fmt.Errorf("%w: key_expiry_ms", ErrInternalSignature)
	case len(c.KeyName) > 256:
		// t_api_key.key_name is varchar(64); 256 bytes is the utf8mb4 ceiling.
		return fmt.Errorf("%w: key_name", ErrInternalSignature)
	case strings.ContainsAny(c.KeyName, "\r\n\x00"):
		// A newline inside a field would make the "\n"-joined canonical
		// ambiguous. Control-plane names are display text, not framing.
		return fmt.Errorf("%w: key_name", ErrInternalSignature)
	case c.GroupCode == "":
		return fmt.Errorf("%w: group_code", ErrInternalSignature)
	case len(c.GroupCode) > 64 || !isGroupCode(c.GroupCode):
		return fmt.Errorf("%w: group_code", ErrInternalSignature)
	}
	return nil
}

// isGroupCode accepts the identifier shape used by t_price_group.code. The
// value is signed, so this is a hygiene check rather than an authorization one.
func isGroupCode(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// internalCanonical returns the exact byte string from T27 §5 constraint 2.
// The final field is not followed by a newline.
func internalCanonical(tsSec int64, bodySHA256Hex string, c InternalClaims) string {
	var b strings.Builder
	b.Grow(256)
	b.WriteString(InternalSigVersion)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(tsSec, 10))
	b.WriteByte('\n')
	b.WriteString(bodySHA256Hex)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(c.UserID, 10))
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(c.KeyID, 10))
	b.WriteByte('\n')
	b.WriteString(c.KeyName)
	b.WriteByte('\n')
	b.WriteString(c.GroupCode)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(c.GroupRatioBP, 10))
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(c.IPMRuleVer, 10))
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(c.KeyExpiryMS, 10))
	return b.String()
}

func internalMAC(secret []byte, canonical string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, canonical)
	return mac.Sum(nil)
}

// SignInternal returns the X-Yunshu-Internal-Sig value:
//
//	v1:<ts_sec>:<base64(HMAC-SHA256(secret, canonical))>
func SignInternal(secret []byte, tsSec int64, body []byte, c InternalClaims) (string, error) {
	if len(secret) < InternalMinSecretBytes {
		return "", fmt.Errorf("%w: secret too short", ErrInternalSignature)
	}
	if err := c.Validate(); err != nil {
		return "", err
	}
	sig := base64.StdEncoding.EncodeToString(
		internalMAC(secret, internalCanonical(tsSec, BodyHashOf(body), c)))
	return InternalSigVersion + ":" + strconv.FormatInt(tsSec, 10) + ":" + sig, nil
}

// SetInternalHeaders writes the seven claim headers plus the signature. It
// removes any pre-existing copy first so a caller that reuses a header map
// cannot create a duplicate, which VerifyInternal rejects.
//
// KeyName travels base64url-encoded because net/http refuses to send a header
// value containing CR or LF. The encoding is transport only: the canonical is
// built from the decoded UTF-8 bytes exactly as the contract states.
func SetInternalHeaders(h http.Header, secret []byte, tsSec int64, body []byte, c InternalClaims) error {
	if h == nil {
		return fmt.Errorf("%w: nil header", ErrInternalSignature)
	}
	sig, err := SignInternal(secret, tsSec, body, c)
	if err != nil {
		return err
	}
	setSingle(h, InternalUserIDHeader, strconv.FormatInt(c.UserID, 10))
	setSingle(h, InternalKeyIDHeader, strconv.FormatInt(c.KeyID, 10))
	setSingle(h, InternalKeyNameHeader, base64.RawURLEncoding.EncodeToString([]byte(c.KeyName)))
	setSingle(h, InternalGroupHeader, c.GroupCode)
	setSingle(h, InternalRatioHeader, strconv.FormatInt(c.GroupRatioBP, 10))
	setSingle(h, InternalIPMHeader, strconv.FormatInt(c.IPMRuleVer, 10))
	setSingle(h, InternalExpiryHeader, strconv.FormatInt(c.KeyExpiryMS, 10))
	setSingle(h, InternalSigHeader, sig)
	return nil
}

func setSingle(h http.Header, name, value string) {
	h.Del(name)
	h.Set(name, value)
}

// VerifyInternal checks the signature and returns the claims. Every failure
// returns an error wrapping ErrInternalSignature; the caller must not branch on
// the wrapped text when writing the response.
//
// The order is: parse the header shape, enforce the ±60s window, rebuild the
// canonical from the transmitted claims, compare with hmac.Equal, then validate
// the claim ranges. A stale timestamp is rejected before any HMAC work, and a
// valid MAC over out-of-range claims is still rejected by Validate.
func VerifyInternal(secret []byte, nowSec int64, body []byte, h http.Header) (InternalClaims, error) {
	var zero InternalClaims
	if len(secret) < InternalMinSecretBytes {
		return zero, fmt.Errorf("%w: secret unavailable", ErrInternalSignature)
	}
	raw, ok := singleHeader(h, InternalSigHeader)
	if !ok {
		return zero, fmt.Errorf("%w: missing signature header", ErrInternalSignature)
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 3 || parts[0] != InternalSigVersion {
		return zero, fmt.Errorf("%w: malformed signature header", ErrInternalSignature)
	}
	tsSec, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return zero, fmt.Errorf("%w: malformed timestamp", ErrInternalSignature)
	}
	if tsSec < nowSec-InternalSigWindowSeconds || tsSec > nowSec+InternalSigWindowSeconds {
		return zero, fmt.Errorf("%w: timestamp outside window", ErrInternalSignature)
	}

	claims, err := claimsFromHeaders(h)
	if err != nil {
		return zero, err
	}
	expected := base64.StdEncoding.EncodeToString(
		internalMAC(secret, internalCanonical(tsSec, BodyHashOf(body), claims)))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return zero, fmt.Errorf("%w: mac mismatch", ErrInternalSignature)
	}
	if err := claims.Validate(); err != nil {
		return zero, err
	}
	return claims, nil
}

func claimsFromHeaders(h http.Header) (InternalClaims, error) {
	var c InternalClaims
	var err error

	if c.UserID, err = headerInt(h, InternalUserIDHeader); err != nil {
		return c, err
	}
	if c.KeyID, err = headerInt(h, InternalKeyIDHeader); err != nil {
		return c, err
	}
	if c.GroupRatioBP, err = headerInt(h, InternalRatioHeader); err != nil {
		return c, err
	}
	if c.IPMRuleVer, err = headerInt(h, InternalIPMHeader); err != nil {
		return c, err
	}
	if c.KeyExpiryMS, err = headerInt(h, InternalExpiryHeader); err != nil {
		return c, err
	}

	name, ok := singleHeader(h, InternalKeyNameHeader)
	if !ok {
		return c, fmt.Errorf("%w: key_name header", ErrInternalSignature)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(name)
	if err != nil {
		return c, fmt.Errorf("%w: key_name encoding", ErrInternalSignature)
	}
	c.KeyName = string(decoded)

	group, ok := singleHeader(h, InternalGroupHeader)
	if !ok {
		return c, fmt.Errorf("%w: group_code header", ErrInternalSignature)
	}
	c.GroupCode = group
	return c, nil
}

func headerInt(h http.Header, name string) (int64, error) {
	v, ok := singleHeader(h, name)
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrInternalSignature, name)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrInternalSignature, name)
	}
	return n, nil
}

// singleHeader returns the value only when the header appears exactly once. A
// duplicated claim header is rejected rather than resolved by first-wins, so an
// intermediary cannot append a second identity that this process might read
// differently from the one that was signed.
func singleHeader(h http.Header, name string) (string, bool) {
	values := h.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}
