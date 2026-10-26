package units

import "testing"

// A10 / F8-2: 1 元 = 100 积分, 1 积分 = 10,000 微积分.
func TestCreditAndCNYFormatting(t *testing.T) {
	cases := []struct {
		micro   int64
		credits string
		cny     string
	}{
		{0, "0", "0"},
		{1, "0.0001", "0.000001"},
		{1500, "0.15", "0.0015"},
		{10_000, "1", "0.01"},
		{9_974_800, "997.48", "9.9748"},
		{1_000_000, "100", "1"},
		{-1500, "-0.15", "-0.0015"},
	}
	for _, tc := range cases {
		if got := Credits(tc.micro); got != tc.credits {
			t.Errorf("Credits(%d) = %q, want %q", tc.micro, got, tc.credits)
		}
		if got := CNY(tc.micro); got != tc.cny {
			t.Errorf("CNY(%d) = %q, want %q", tc.micro, got, tc.cny)
		}
	}
}
