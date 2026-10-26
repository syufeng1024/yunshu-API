package ratelimit

import "testing"

func TestPlaceholderTokens(t *testing.T) {
	tests := []struct {
		name      string
		input     int64
		maxTokens int64
		modelMax  int64
		want      int64
	}{
		{"explicit max_tokens", 800, 600, 4096, 1400},
		{"missing max_tokens uses model cap", 800, 0, 4096, 4896},
		{"negative input clamped", -5, 100, 4096, 100},
		{"negative max_tokens falls back", 100, -1, 512, 612},
	}
	for _, tt := range tests {
		if got := PlaceholderTokens(tt.input, tt.maxTokens, tt.modelMax); got != tt.want {
			t.Errorf("%s: got %d want %d", tt.name, got, tt.want)
		}
	}
}

func TestSettleTokens(t *testing.T) {
	// usage overrides the placeholder.
	actual, delta := SettleTokens(Usage{Present: true, PromptTokens: 500, CompletionTokens: 300}, 0, 0, 1000)
	if actual != 800 || delta != -200 {
		t.Fatalf("usage settle = (%d,%d) want (800,-200)", actual, delta)
	}

	// usage over the placeholder produces a positive delta.
	actual, delta = SettleTokens(Usage{Present: true, PromptTokens: 700, CompletionTokens: 500}, 0, 0, 1000)
	if actual != 1200 || delta != 200 {
		t.Fatalf("overrun settle = (%d,%d) want (1200,200)", actual, delta)
	}

	// No usage: emitted + estimated input, never a full release.
	actual, delta = SettleTokens(Usage{Present: false}, 200, 100, 1000)
	if actual != 300 || delta != -700 {
		t.Fatalf("no-usage settle = (%d,%d) want (300,-700)", actual, delta)
	}

	// A zero-usage upstream response is still authoritative.
	actual, delta = SettleTokens(Usage{Present: true}, 0, 0, 1000)
	if actual != 0 || delta != -1000 {
		t.Fatalf("zero-usage settle = (%d,%d) want (0,-1000)", actual, delta)
	}
}

func TestUsageTotalIncludesBothSides(t *testing.T) {
	u := Usage{Present: true, PromptTokens: 800, CompletionTokens: 600}
	if u.Total() != 1400 {
		t.Fatalf("Total = %d want 1400", u.Total())
	}
	if (Usage{Present: false, PromptTokens: 1, CompletionTokens: 1}).Total() != 0 {
		t.Fatal("absent usage must not report tokens")
	}
}
