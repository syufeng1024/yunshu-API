package billing

import "testing"

func TestRoundUpDiv(t *testing.T) {
	cases := []struct{ n, d, want int64 }{
		{0, 1000, 0},
		{1, 1000, 1},
		{998, 1000, 1},
		{1000, 1000, 1},
		{1001, 1000, 2},
		{1500, 1000, 2},
		{-5, 1000, 0},
		{10, 0, 0},
	}
	for _, c := range cases {
		if got := RoundUpDiv(c.n, c.d); got != c.want {
			t.Fatalf("RoundUpDiv(%d,%d)=%d, want %d", c.n, c.d, got, c.want)
		}
	}
}

func TestThreeWayConversion(t *testing.T) {
	if got := CreditsToMicro(7900); got != 79000000 {
		t.Fatalf("CreditsToMicro(7900)=%d, want 79000000", got)
	}
	if got := MicroToCredits(79000000); got != 7900 {
		t.Fatalf("MicroToCredits(79000000)=%d, want 7900", got)
	}
	if got := YuanToMicro(100); got != 100000000 {
		t.Fatalf("YuanToMicro(100)=%d, want 100000000", got)
	}
	if got := FenToMicro(1000); got != 10000000 {
		t.Fatalf("FenToMicro(1000)=%d, want 10000000", got)
	}
	if got := MicroToFen(79000000); got != 7900 {
		t.Fatalf("MicroToFen(79000000)=%d, want 7900", got)
	}
	if got := MicroToYuan(100000000); got != 100 {
		t.Fatalf("MicroToYuan(100000000)=%d, want 100", got)
	}
}

func TestDisplayFormattingUsesIntegers(t *testing.T) {
	if got := FormatCredits(1500); got != "0.1500" {
		t.Fatalf("FormatCredits(1500)=%q, want 0.1500", got)
	}
	if got := FormatCredits(-500000); got != "-50.0000" {
		t.Fatalf("FormatCredits(-500000)=%q, want -50.0000", got)
	}
	// 7,900 credits = 79 yuan; 90% = 71.10 yuan = 71,100,000 micro.
	if got := FormatYuan(71100000); got != "71.10" {
		t.Fatalf("FormatYuan(71100000)=%q, want 71.10", got)
	}
	if got := FormatYuan(-500000); got != "-0.50" {
		t.Fatalf("FormatYuan(-500000)=%q, want -0.50", got)
	}
}
