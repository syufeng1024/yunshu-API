package mask

import "testing"

// A-附加2: every mask function must keep an empty value empty, mask a too-short
// value in full, and apply its window to a normal value.
func TestMaskFunctions(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) string
		in   string
		want string
	}{
		{"phone/empty", MaskPhone, "", ""},
		{"phone/too short", MaskPhone, "1234567", "******"},
		{"phone/normal", MaskPhone, "13812348888", "138****8888"},
		{"phone/boundary", MaskPhone, "12345678", "123*5678"},

		{"email/empty", MaskEmail, "", ""},
		{"email/no at", MaskEmail, "not-an-email", "******"},
		{"email/empty local", MaskEmail, "@qq.com", "******"},
		{"email/empty domain", MaskEmail, "alice@", "******"},
		{"email/normal", MaskEmail, "alice@qq.com", "a***@qq.com"},
		{"email/single char local", MaskEmail, "a@qq.com", "a***@qq.com"},

		{"idcard/empty", MaskIDCard, "", ""},
		{"idcard/too short", MaskIDCard, "11010190030", "******"},
		{"idcard/normal", MaskIDCard, "110101199003071234", "110101********1234"},
		{"idcard/boundary", MaskIDCard, "110101900307", "110101**0307"},

		{"bank/empty", MaskBankCard, "", ""},
		{"bank/too short", MaskBankCard, "1234567", "******"},
		{"bank/normal", MaskBankCard, "6222021234567890", "************7890"},
		{"bank/boundary", MaskBankCard, "12345678", "****5678"},

		{"upstream/empty", MaskUpstreamKey, "", ""},
		{"upstream/normal", MaskUpstreamKey, "sk-live-abc123", "sk-****"},
		{"upstream/short", MaskUpstreamKey, "x", "sk-****"},

		{"apikey/empty", MaskAPIKey, "", ""},
		{"apikey/too short", MaskAPIKey, "sk-abc", "******"},
		{"apikey/exactly 8", MaskAPIKey, "sk-abc12", "******"},
		{"apikey/normal", MaskAPIKey, "sk-abcdef123456", "sk-abcde…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(tc.in); got != tc.want {
				t.Fatalf("%s(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"authorization header", "Authorization: Bearer sk-abc123", "Authorization: ****"},
		{"bearer only", "client says Bearer sk-abc123 here", "client says Bearer **** here"},
		{"bare sk key", "sk-abc123", "sk-****"},
		{"empty", "", ""},
		{"no secret", "gpt-4o", "gpt-4o"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
