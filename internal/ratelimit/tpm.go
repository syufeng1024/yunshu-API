package ratelimit

// Usage is the upstream token report. Present distinguishes "the upstream
// said zero" from "the upstream said nothing": the latter must fall back to
// local accounting instead of releasing the placeholder.
type Usage struct {
	Present          bool
	PromptTokens     int64
	CompletionTokens int64
}

// Total returns prompt + completion. TPM measures the sum, not either side
// (T06 hard constraint 8 / PRD §3.2).
func (u Usage) Total() int64 {
	if !u.Present {
		return 0
	}
	return nonNegative(u.PromptTokens) + nonNegative(u.CompletionTokens)
}

// PlaceholderTokens computes the pre-request reservation:
//
//	input tokens + max_tokens (or the model's output cap when max_tokens is
//	absent).
//
// Input tokens come from the local tokenizer; this function only applies the
// formula and never estimates by itself.
func PlaceholderTokens(inputTokens, maxTokens, modelMaxOutput int64) int64 {
	output := maxTokens
	if output <= 0 {
		output = modelMaxOutput
	}
	return nonNegative(inputTokens) + nonNegative(output)
}

// SettleTokens returns the counter value after a request and the delta to
// add to the same minute bucket.
//
// With usage: the reported total *overwrites* the placeholder, so the delta
// is actual - occupied and may be negative.
//
// Without usage: actual = emitted output + estimated input. The placeholder
// is never released wholesale — a request that returns no usage must not
// become a rate-limit exemption (T06 hard constraint 8⑤).
func SettleTokens(usage Usage, estimatedInput, emittedOutput, occupied int64) (actual, delta int64) {
	if usage.Present {
		actual = usage.Total()
	} else {
		actual = nonNegative(emittedOutput) + nonNegative(estimatedInput)
	}
	return actual, actual - nonNegative(occupied)
}

// SettleAfterRelease is the adjustment to hand to Limiter.Release: the TPM
// delta for the acquire-minute bucket. Cross-minute suppression lives in the
// Lua script, not here, so the caller always passes the computed delta.
func SettleAfterRelease(usage Usage, estimatedInput, emittedOutput, occupied int64) int64 {
	_, delta := SettleTokens(usage, estimatedInput, emittedOutput, occupied)
	return delta
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
