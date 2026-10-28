package apierr

import "strings"

// W3C trace-context widths.
const (
	traceparentVersion = "00"
	traceIDHexLen      = 32
	spanIDHexLen       = 16
	traceFlagsHexLen   = 2
)

// ParseTraceparent validates a W3C traceparent header of the form
// 00-<32hex>-<16hex>-<2hex> and returns its trace-id. An invalid value is not
// an error: the gateway ignores it and keeps the ordinary request-id path
// (T22 §12.4).
//
// The hex check is lowercase and the all-zero trace-id is rejected, matching
// the W3C grammar and the pre-existing httpsrv.traceIDFromHeaders parser. Two
// definitions of "valid traceparent" in one process would otherwise make the
// legacy X-Trace-Id header and the new request line disagree.
func ParseTraceparent(v string) (string, bool) {
	v = strings.TrimSpace(v)
	parts := strings.Split(v, "-")
	if len(parts) != 4 {
		return "", false
	}
	if parts[0] != traceparentVersion {
		return "", false
	}
	if !isLowerHex(parts[1], traceIDHexLen) || allZero(parts[1]) {
		return "", false
	}
	if !isLowerHex(parts[2], spanIDHexLen) || !isLowerHex(parts[3], traceFlagsHexLen) {
		return "", false
	}
	return parts[1], true
}

// ResolveRequestID is the gateway entry decision from T22 §12.4. A client
// X-Request-Id wins (existing sanitizeRequestID rules); otherwise a valid
// traceparent supplies the id; otherwise a fresh UUIDv7 is generated. The
// returned value is both Ctx.RequestID and the log trace_id.
func ResolveRequestID(xRequestID, traceparent string) string {
	if id := sanitizeRequestID(xRequestID); id != "" {
		return id
	}
	if tid, ok := ParseTraceparent(traceparent); ok {
		return tid
	}
	return NewRequestID()
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func allZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
