package apierr

import (
	"math"
	"strconv"
	"strings"
)

// NewInsufficientCredits builds the 402 used when the pre-hold exceeds the
// available balance. rawEstimate is the 402 decision number (`raw_est`, not
// `est`). remaining is the available balance in credits and may be negative;
// limit is the overdraft ceiling. No reset_at is ever added.
//
// The three numbers in the message are the ones E.2 requires: raw_est,
// available and the shortfall. When the shortfall is negative the message
// points at concurrency/RPM instead (PRD E.1.4, E-2).
func NewInsufficientCredits(rawEstimate int64, available, overdraftLimit float64) *APIError {
	e := Newf(CodeInsufficientCredits)
	limit := overdraftLimit
	remaining := available
	e.Limit = &limit
	e.Remaining = &remaining

	shortfall := float64(rawEstimate) - available
	if shortfall < 0 {
		e.Message = "本次请求预估需要 " + formatCredits(float64(rawEstimate)) +
			" 积分，当前可用余额 " + formatCredits(available) +
			"（含透支上限 " + formatCredits(overdraftLimit) +
			"），可用余额充足，请检查是否触发了并发 / RPM 限制"
		return e
	}
	e.Message = "本次请求预估需要 " + formatCredits(float64(rawEstimate)) +
		" 积分，当前可用余额 " + formatCredits(available) +
		"（含透支上限 " + formatCredits(overdraftLimit) +
		"），差额 " + formatCredits(shortfall)
	return e
}

// NewFrozenDueToOverdraft builds the 402 used when the account crossed the
// overdraft ceiling and t_arrears has a debt. overdrawn is a positive number
// (the debt magnitude); the body reports remaining = -overdrawn.
//
// need = arrears + (limit - overdrawn), so a single top-up of that amount
// unfreezes the account instead of requiring a second payment.
func NewFrozenDueToOverdraft(overdrawn, overdraftLimit float64, arrears int64) *APIError {
	e := Newf(CodeAccountFrozenDueToOverdraft)
	limit := overdraftLimit
	remaining := -math.Abs(overdrawn)
	e.Limit = &limit
	e.Remaining = &remaining

	need := overdraftLimit - math.Abs(overdrawn)
	if arrears > 0 {
		need += float64(arrears)
	}
	if need < 0 {
		need = 0
	}
	e.Message = "已透支 " + formatCredits(math.Abs(overdrawn)) +
		" 积分、透支上限 " + formatCredits(overdraftLimit) +
		"，需充值 " + formatCredits(need) + " 以上才恢复"
	return e
}

// formatCredits renders credits for humans: integer grouping for whole
// numbers, at most two decimals for fractional balances, no exponent.
func formatCredits(v float64) string {
	rounded := math.Round(v*100) / 100
	if rounded == 0 {
		return "0"
	}
	negative := rounded < 0
	magnitude := math.Abs(rounded)
	whole := int64(magnitude)
	frac := magnitude - float64(whole)
	out := groupThousands(whole)
	if frac != 0 {
		fracStr := strconv.FormatFloat(frac, 'f', 2, 64)
		fracStr = strings.TrimRight(fracStr, "0")
		fracStr = strings.TrimPrefix(fracStr, "0")
		out += fracStr
	}
	if negative {
		return "-" + out
	}
	return out
}

func groupThousands(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
