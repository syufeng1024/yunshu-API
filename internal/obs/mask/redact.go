package mask

import "regexp"

// Redact strips credential-shaped substrings from free text before it is
// written to a log line. The request schema has no user_agent field, so this is
// how a client-supplied string (a model alias, an upstream error echo) that
// happens to embed "Authorization: Bearer sk-…" is prevented from carrying the
// credential into the log. The primary defence — not passing keys to the
// logger — is unchanged.
var (
	authHeaderRE = regexp.MustCompile(`(?i)\b(authorization|x-api-key|api[_-]?key)\b\s*[:=]\s*(?:bearer\s+)?[^\s,;]+`)
	bearerRE     = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
	skKeyRE      = regexp.MustCompile(`\bsk-[A-Za-z0-9._-]+`)
)

// Redact replaces credential-shaped substrings with fixed masks.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	s = authHeaderRE.ReplaceAllString(s, "$1: ****")
	s = bearerRE.ReplaceAllString(s, "Bearer ****")
	s = skKeyRE.ReplaceAllString(s, "sk-****")
	return s
}
