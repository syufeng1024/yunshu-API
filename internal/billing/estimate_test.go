package billing

import (
	"errors"
	"testing"
)

type fixedCounter struct {
	tokens int64
	err    error
}

func (f fixedCounter) CountInput(string) (int64, error) { return f.tokens, f.err }

func TestEstimateTokensUsesMaxTokensOrModelCeiling(t *testing.T) {
	counter := fixedCounter{tokens: 20000}

	explicit := int64(5000)
	got, err := EstimateTokens(counter, "prompt", &explicit, 8192, 11000)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	if got.OutputTokens != 5000 {
		t.Fatalf("output tokens = %d, want 5000", got.OutputTokens)
	}

	got, err = EstimateTokens(counter, "prompt", nil, 8192, 11000)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	if got.OutputTokens != 8192 {
		t.Fatalf("output tokens = %d, want model ceiling 8192", got.OutputTokens)
	}

	if _, err := EstimateTokens(counter, "prompt", nil, 0, 11000); !errors.Is(err, ErrNoMaxOutput) {
		t.Fatalf("err = %v, want ErrNoMaxOutput", err)
	}
	if _, err := EstimateTokens(nil, "prompt", &explicit, 8192, 11000); !errors.Is(err, ErrNoInputCounter) {
		t.Fatalf("err = %v, want ErrNoInputCounter", err)
	}
}

func TestEstimateTokensAppliesSafetyAtTokenStage(t *testing.T) {
	got, err := EstimateTokens(fixedCounter{tokens: 20000}, "prompt", ptr(5000), 0, 11000)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	if got.SafeInputTokens != 22000 || got.SafeOutputTokens != 5500 {
		t.Fatalf("safe tokens = %d/%d, want 22000/5500", got.SafeInputTokens, got.SafeOutputTokens)
	}

	// Ceiling, not truncation: 1 × 1.10 = 1.1 rounds up to 2.
	one, err := EstimateTokens(fixedCounter{tokens: 1}, "prompt", ptr(1), 0, 11000)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	if one.SafeInputTokens != 2 || one.SafeOutputTokens != 2 {
		t.Fatalf("safe tokens = %d/%d, want 2/2", one.SafeInputTokens, one.SafeOutputTokens)
	}
}

// The discriminator between "two independent margins" and "one merged
// coefficient": raw_est has no margin at all, est applies safety at the token
// stage and prehold at the amount stage.
func TestRawAndEstimateAreIndependent(t *testing.T) {
	raw := RawEstimateMicro(20000, 5000, 1500, 4500)
	if raw != 52500 {
		t.Fatalf("raw estimate = %d, want 52500", raw)
	}

	est, err := EstimateMicro(20000, 5000, 1500, 4500, 11000, 12000)
	if err != nil {
		t.Fatalf("EstimateMicro: %v", err)
	}
	// 22,000 × 1,500 + 5,500 × 4,500 = 57,750,000 micro; ×1.2 = 69,300.
	if est != 69300 {
		t.Fatalf("estimate = %d, want 69300", est)
	}

	// A naive `est = raw_est × 1.2` would be 63,000. It must not match.
	if est == raw*12000/BasisPoints {
		t.Fatalf("estimate collapsed to raw × prehold_ratio: %d", est)
	}

	// Safety only: 57,750.
	estSafetyOnly, err := EstimateMicro(20000, 5000, 1500, 4500, 11000, 10000)
	if err != nil {
		t.Fatalf("EstimateMicro: %v", err)
	}
	if estSafetyOnly != 57750 {
		t.Fatalf("safety-only estimate = %d, want 57750", estSafetyOnly)
	}

	// Prehold only: 63,000.
	estPreholdOnly, err := EstimateMicro(20000, 5000, 1500, 4500, 10000, 12000)
	if err != nil {
		t.Fatalf("EstimateMicro: %v", err)
	}
	if estPreholdOnly != 63000 {
		t.Fatalf("prehold-only estimate = %d, want 63000", estPreholdOnly)
	}
}

func TestPreestimateRequestReturnsBothNumbers(t *testing.T) {
	maxTokens := int64(5000)
	got, err := PreestimateRequest(
		fixedCounter{tokens: 20000}, "prompt", &maxTokens, 0,
		1500, 4500, 11000, 12000,
	)
	if err != nil {
		t.Fatalf("PreestimateRequest: %v", err)
	}
	if got.RawMicro != 52500 || got.EstMicro != 69300 {
		t.Fatalf("preestimate = raw %d / est %d, want 52500 / 69300", got.RawMicro, got.EstMicro)
	}
	if got.Tokens.InputTokens != 20000 || got.Tokens.OutputTokens != 5000 {
		t.Fatalf("tokens = %d/%d, want 20000/5000", got.Tokens.InputTokens, got.Tokens.OutputTokens)
	}
}

func TestInvalidSafetyBPRejected(t *testing.T) {
	if _, err := EstimateMicro(1, 1, 1000, 1000, 5000, 12000); !errors.Is(err, ErrInvalidSafetyBP) {
		t.Fatalf("err = %v, want ErrInvalidSafetyBP", err)
	}
}

func ptr(v int64) *int64 { return &v }
