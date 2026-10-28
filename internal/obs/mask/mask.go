// Package mask holds the second line of defence for user-controlled content
// that reaches a log line (TDS §5.1). The first line remains "never hand the
// plaintext to the logger at all"; these functions exist for values the caller
// cannot control, such as a phone number a client typed into User-Agent.
//
// Two rules apply to every function:
//   - the empty string stays empty; masking "no value" into "******" would make
//     the log look as if a value existed and had been masked;
//   - a value shorter than the rule's window is masked in full. Keeping the
//     first N and last M characters of a 5-character value would reveal all of
//     it.
package mask

import "strings"

// fullyMasked is the replacement for the whole value when the rule cannot be
// applied safely.
const fullyMasked = "******"

// MaskPhone keeps the first three and last four digits: 138****8888.
func MaskPhone(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 8 {
		return fullyMasked
	}
	return s[:3] + strings.Repeat("*", len(s)-7) + s[len(s)-4:]
}

// MaskEmail keeps the first character of the local part: a***@qq.com.
func MaskEmail(s string) string {
	if s == "" {
		return ""
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return fullyMasked
	}
	return s[:1] + "***" + s[at:]
}

// MaskIDCard keeps the first six and last four characters.
func MaskIDCard(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 12 {
		return fullyMasked
	}
	return s[:6] + strings.Repeat("*", len(s)-10) + s[len(s)-4:]
}

// MaskBankCard keeps the last four characters only.
func MaskBankCard(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 8 {
		return fullyMasked
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

// MaskUpstreamKey renders any channel credential as the same fixed token, so
// the log cannot be used to distinguish one upstream key from another.
func MaskUpstreamKey(s string) string {
	if s == "" {
		return ""
	}
	return "sk-****"
}

// MaskAPIKey keeps the first eight characters: sk-abcde…
func MaskAPIKey(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return fullyMasked
	}
	return s[:8] + "…"
}
