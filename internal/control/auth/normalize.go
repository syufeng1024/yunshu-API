package auth

import (
	"errors"
	"strings"
)

// Identifier kinds. The column that stores the value is chosen from this, not
// from a client-supplied field.
const (
	KindEmail = "email"
	KindPhone = "phone"
)

// Identifier is a normalized email or phone.
type Identifier struct {
	Kind  string
	Value string
}

// NormalizeIdentifier applies the input-cleanup rule before any lookup:
// trim, full-width to half-width, remove formatting characters from a phone,
// lowercase an email. Without the lowercase step utf8mb4_0900_as_cs would let
// Alice@example.com and alice@example.com register as two accounts.
func NormalizeIdentifier(raw string) (Identifier, error) {
	s := strings.TrimSpace(toHalfWidth(raw))
	if s == "" {
		return Identifier{}, errors.New("empty identifier")
	}
	if strings.ContainsAny(s, " \t\r\n") && !strings.Contains(s, "@") {
		s = stripPhoneSeparators(s)
	}
	if strings.Contains(s, "@") {
		email := strings.ToLower(s)
		if len(email) > 254 {
			return Identifier{}, errors.New("email exceeds 254 characters")
		}
		at := strings.IndexByte(email, '@')
		if at <= 0 || at == len(email)-1 {
			return Identifier{}, errors.New("email is missing a local part or domain")
		}
		if strings.Count(email, "@") != 1 {
			return Identifier{}, errors.New("email contains more than one @")
		}
		domain := email[at+1:]
		if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
			return Identifier{}, errors.New("email domain is malformed")
		}
		for _, r := range email {
			if r <= 0x20 || r == 0x7f {
				return Identifier{}, errors.New("email contains whitespace or a control character")
			}
		}
		return Identifier{Kind: KindEmail, Value: email}, nil
	}

	phone := stripPhoneSeparators(s)
	if len(phone) < 6 || len(phone) > 20 {
		return Identifier{}, errors.New("phone must be 6 to 20 digits")
	}
	for i := 0; i < len(phone); i++ {
		if phone[i] < '0' || phone[i] > '9' {
			return Identifier{}, errors.New("phone contains a non-digit")
		}
	}
	return Identifier{Kind: KindPhone, Value: phone}, nil
}

// toHalfWidth converts full-width ASCII and the ideographic space, so a
// pasted identifier from an IME matches the ASCII row.
func toHalfWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x3000:
			b.WriteRune(' ')
		case r >= 0xFF01 && r <= 0xFF5E:
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stripPhoneSeparators removes the characters humans insert in phone numbers.
// A leading + is dropped because t_user.phone stores E.164 without it.
func stripPhoneSeparators(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n', '-', '(', ')', '+', '.':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// maskIdentifier keeps enough of the identifier for an operator to recognise
// the account while keeping the audit trail from becoming a PII dump.
func maskIdentifier(kind, value string) string {
	if value == "" {
		return ""
	}
	if kind == KindEmail {
		at := strings.IndexByte(value, '@')
		if at <= 0 {
			return "***"
		}
		local := value[:at]
		if len(local) <= 1 {
			return "*" + value[at:]
		}
		return local[:1] + "***" + value[at:]
	}
	if len(value) <= 4 {
		return "***"
	}
	return value[:3] + "****" + value[len(value)-2:]
}
