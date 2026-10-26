package billing

import "errors"

// Estimate errors.
var (
	ErrNoInputCounter  = errors.New("billing: input token counter is nil")
	ErrNoMaxOutput     = errors.New("billing: max_tokens and model output ceiling are both unset")
	ErrInvalidSafetyBP = errors.New("billing: safety basis points must be >= 10000")
)

// InputCounter is the local tokenizer boundary. T07 supplies the concrete
// tokenizer; T04 only defines the seam so the estimate rules are testable
// without an upstream call. Implementations must count the request input
// exactly once and must not apply any coefficient.
type InputCounter interface {
	CountInput(text string) (int64, error)
}

// TokenEstimate holds both the raw token counts and the safety-inflated
// counts. Keeping them separate is deliberate: raw_est and est must never be
// derived from one another by a single multiplier.
type TokenEstimate struct {
	InputTokens      int64
	OutputTokens     int64
	SafeInputTokens  int64
	SafeOutputTokens int64
	SafetyBP         int64
}

// EstimateTokens applies the input-estimation rules:
//
//  1. input tokens come from the local tokenizer (exact, no character heuristic);
//  2. language coefficient is 1.0 in V1.0 (LangCoefBP), so it is not a factor;
//  3. output tokens = the request's max_tokens, or the model's output ceiling;
//  4. the safety basis points are applied at the TOKEN stage, upward.
//
// prehold_ratio is not accepted here: it is an amount-stage margin and is
// applied by EstimateMicro. The two margins must not be collapsed.
func EstimateTokens(
	counter InputCounter,
	input string,
	maxTokens *int64,
	modelMaxOutput int64,
	safetyBP int64,
) (TokenEstimate, error) {
	if counter == nil {
		return TokenEstimate{}, ErrNoInputCounter
	}
	if safetyBP < BasisPoints {
		return TokenEstimate{}, ErrInvalidSafetyBP
	}
	in, err := counter.CountInput(input)
	if err != nil {
		return TokenEstimate{}, err
	}
	if in < 0 {
		return TokenEstimate{}, ErrNegativeTokens
	}

	var out int64
	switch {
	case maxTokens != nil:
		out = *maxTokens
	case modelMaxOutput > 0:
		out = modelMaxOutput
	default:
		return TokenEstimate{}, ErrNoMaxOutput
	}
	if out < 0 {
		return TokenEstimate{}, ErrNegativeTokens
	}

	return TokenEstimate{
		InputTokens:      in,
		OutputTokens:     out,
		SafeInputTokens:  RoundUpDiv(in*safetyBP, BasisPoints),
		SafeOutputTokens: RoundUpDiv(out*safetyBP, BasisPoints),
		SafetyBP:         safetyBP,
	}, nil
}

// RawEstimateMicro is the admission estimate. It prices the tokenizer input
// and the requested max_tokens output WITHOUT any margin. Admission compares
// this value with the available balance (G-02).
func RawEstimateMicro(tokensIn, tokensOut, inPriceMicroPerK, outPriceMicroPerK int64) int64 {
	return CostMicro(tokensIn, tokensOut, inPriceMicroPerK, outPriceMicroPerK)
}

// EstimateMicro is the prehold estimate. It prices the safety-inflated token
// counts first, then applies the prehold ratio to the amount. The safety
// margin and the prehold margin are applied in two different stages and are
// never merged into one coefficient.
func EstimateMicro(
	tokensIn, tokensOut, inPriceMicroPerK, outPriceMicroPerK, safetyBP, preholdRatioBP int64,
) (int64, error) {
	if safetyBP < BasisPoints {
		return 0, ErrInvalidSafetyBP
	}
	safeIn := RoundUpDiv(tokensIn*safetyBP, BasisPoints)
	safeOut := RoundUpDiv(tokensOut*safetyBP, BasisPoints)
	base := CostMicro(safeIn, safeOut, inPriceMicroPerK, outPriceMicroPerK)
	return ApplyRateBp(base, preholdRatioBP)
}

// Preestimate is the two-number result the gateway needs before the upstream
// call: RawMicro for the 402 check, EstMicro for the hold amount.
type Preestimate struct {
	Tokens            TokenEstimate
	RawMicro          int64
	EstMicro          int64
	InPriceMicroPerK  int64
	OutPriceMicroPerK int64
}

// PreestimateRequest combines token estimation with the two independent
// amount estimates. The caller supplies the group-adjusted or standard unit
// prices; this function does not read any configuration.
func PreestimateRequest(
	counter InputCounter,
	input string,
	maxTokens *int64,
	modelMaxOutput int64,
	inPriceMicroPerK, outPriceMicroPerK int64,
	safetyBP, preholdRatioBP int64,
) (Preestimate, error) {
	tokens, err := EstimateTokens(counter, input, maxTokens, modelMaxOutput, safetyBP)
	if err != nil {
		return Preestimate{}, err
	}
	est, err := EstimateMicro(
		tokens.InputTokens, tokens.OutputTokens,
		inPriceMicroPerK, outPriceMicroPerK,
		safetyBP, preholdRatioBP,
	)
	if err != nil {
		return Preestimate{}, err
	}
	return Preestimate{
		Tokens: tokens,
		RawMicro: RawEstimateMicro(
			tokens.InputTokens, tokens.OutputTokens,
			inPriceMicroPerK, outPriceMicroPerK,
		),
		EstMicro:          est,
		InPriceMicroPerK:  inPriceMicroPerK,
		OutPriceMicroPerK: outPriceMicroPerK,
	}, nil
}
