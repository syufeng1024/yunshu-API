package gw

import (
	"unicode/utf8"
)

// HeuristicCounter is the local tokenizer seam used until a model-specific
// tokenizer is wired. billing.InputCounter exists precisely so the counting
// rule can be replaced without touching the estimate formulas (T04).
//
// The rule: one token per non-ASCII rune (CJK text is roughly one token per
// character) plus one token per four ASCII bytes, rounded up. It is
// deterministic, never returns zero for non-empty input, and is deliberately
// conservative for ASCII code. TDS §3.2 requires an exact tokenizer; that
// dependency is not in go.mod, so this placeholder is reported as a deviation
// for T07 and must be replaced before the first upstream call (T08).
type HeuristicCounter struct{}

// CountInput implements billing.InputCounter.
func (HeuristicCounter) CountInput(text string) (int64, error) {
	var asciiBytes, runes int64
	for _, r := range text {
		if r < utf8.RuneSelf {
			asciiBytes++
			continue
		}
		runes++
	}
	tokens := runes + (asciiBytes+3)/4
	return tokens, nil
}

// LanguageFactorBP is the V1.0 identity table (billing.estimate_language_coef
// default {zh:1.0,en:1.0,code:1.0,mixed:1.0}). It is intentionally not applied:
// the coefficient is 1.0 until the calibration in §7-2 lands.
const LanguageFactorBP = 10000
