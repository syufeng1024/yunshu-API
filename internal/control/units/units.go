// Package units converts the integer micro-credit amounts used by the ledger
// into the two display units the console shows: credits (积分) and CNY.
//
// The conversion is integer-only on purpose. A float64 cannot represent
// 0.1 exactly, and the console's price reference is compared against the
// ledger in acceptance tests; formatting through float would make 1500 micro
// print as 0.14999999999999999 on some paths and 0.15 on others.
package units

import (
	"strconv"
	"strings"
)

// Fixed conversion rates from PRD §3.2.1 / F8-2.
const (
	// MicroPerCredit is 1 积分 = 10,000 微积分 (the ledger's storage unit).
	MicroPerCredit int64 = 10_000
	// CreditsPerYuan is 1 元 = 100 积分 (F8-2's display rule).
	CreditsPerYuan int64 = 100
	// MicroPerYuan is 1 元 = 1,000,000 微积分.
	MicroPerYuan int64 = MicroPerCredit * CreditsPerYuan
)

// Credits renders micro-credits as 积分 with at most four fractional digits.
// 1500 micro → "0.15"; 9974800 micro → "997.48".
func Credits(micro int64) string { return scaled(micro, MicroPerCredit, 4) }

// CNY renders micro-credits as yuan with at most six fractional digits.
// 1500 micro → "0.0015"; 9974800 micro → "0.99748".
func CNY(micro int64) string { return scaled(micro, MicroPerYuan, 6) }

// scaled divides v by scale and trims trailing fractional zeros. It avoids
// negating math.MinInt64 by converting the magnitude through uint64.
func scaled(v, scale int64, digits int) string {
	neg := v < 0
	var mag uint64
	if neg {
		mag = uint64(-(v + 1))
		mag++
	} else {
		mag = uint64(v)
	}
	u := uint64(scale)
	whole := mag / u
	rem := mag % u

	var b strings.Builder
	if neg && (whole > 0 || rem > 0) {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatUint(whole, 10))
	if rem == 0 {
		return b.String()
	}
	frac := strconv.FormatUint(rem, 10)
	if len(frac) < digits {
		frac = strings.Repeat("0", digits-len(frac)) + frac
	}
	frac = strings.TrimRight(frac, "0")
	b.WriteByte('.')
	b.WriteString(frac)
	return b.String()
}
