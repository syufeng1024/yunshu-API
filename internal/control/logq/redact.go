package logq

import "regexp"

// The three sensitive shapes PRD §5.7 / T14 hard constraint 10 names:
// mainland mobile numbers, e-mail addresses and sk- style API keys. A body
// containing one of them is masked, never rejected — refusing the whole body
// would make the console less useful than the raw log.
var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phonePattern = regexp.MustCompile(`\b1[3-9][0-9]{9}\b`)
	// OpenAI and Anthropic keys both start with sk- followed by at least
	// eight key characters. The prefix is kept so the reader can tell what
	// kind of secret was removed.
	apiKeyPattern = regexp.MustCompile(`sk-[A-Za-z0-9_\-]{8,}`)
)

// redactBody returns the masked body and whether anything changed.
func redactBody(body string) (string, bool) {
	out := body
	out = emailPattern.ReplaceAllStringFunc(out, maskEmail)
	out = phonePattern.ReplaceAllStringFunc(out, func(m string) string {
		return maskMiddle(m, 3, 4)
	})
	out = apiKeyPattern.ReplaceAllStringFunc(out, func(m string) string {
		return maskMiddle(m, 3, 4)
	})
	return out, out != body
}

// maskEmail keeps the first character of the local part and the domain:
// "zhangsan@example.com" → "z***@example.com". Keeping the domain matters
// because it is not a secret and tells the support engineer which provider
// rejected the request.
func maskEmail(v string) string {
	at := -1
	for i := 0; i < len(v); i++ {
		if v[i] == '@' {
			at = i
			break
		}
	}
	if at <= 0 {
		return maskMiddle(v, 1, 0)
	}
	local := v[:at]
	domain := v[at:]
	if len(local) <= 1 {
		return "***" + domain
	}
	return local[:1] + "***" + domain
}

// maskMiddle keeps head bytes and tail bytes, replacing the middle with a
// fixed-width asterisk run. Matches are ASCII by construction, so byte
// slicing cannot split a UTF-8 rune.
func maskMiddle(v string, head, tail int) string {
	if len(v) <= head+tail {
		return "***"
	}
	return v[:head] + "****" + v[len(v)-tail:]
}
