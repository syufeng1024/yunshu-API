package logq

import "testing"

func TestRedactBodyMasksOnlySensitiveShapes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "phone",
			in:      "call 13800138000 now",
			want:    "call 138****8000 now",
			changed: true,
		},
		{
			name:    "email keeps domain",
			in:      "to zhangsan@example.com",
			want:    "to z***@example.com",
			changed: true,
		},
		{
			name:    "openai key",
			in:      "Authorization: Bearer sk-abcdefghijklmnop",
			want:    "Authorization: Bearer sk-****mnop",
			changed: true,
		},
		{
			name:    "anthropic key",
			in:      "sk-ant-api03-abcdefghijklmnop",
			want:    "sk-****mnop",
			changed: true,
		},
		{
			name:    "plain text untouched",
			in:      "the model answered in 120 ms",
			want:    "the model answered in 120 ms",
			changed: false,
		},
		{
			name:    "short number is not a phone",
			in:      "order 12345",
			want:    "order 12345",
			changed: false,
		},
	}
	for _, tc := range cases {
		got, changed := redactBody(tc.in)
		if got != tc.want || changed != tc.changed {
			t.Errorf("%s: redactBody(%q) = (%q,%v), want (%q,%v)",
				tc.name, tc.in, got, changed, tc.want, tc.changed)
		}
	}
}

// Masking is idempotent: a masked value must not be masked again, otherwise
// repeated reads would keep eating characters.
func TestRedactBodyIsIdempotent(t *testing.T) {
	once, _ := redactBody("13800138000 zhangsan@example.com sk-abcdefghijklmnop")
	twice, changed := redactBody(once)
	if twice != once || changed {
		t.Fatalf("second pass changed the body: %q -> %q", once, twice)
	}
}
