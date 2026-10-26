// Package billing contains the pure pricing domain: micro-credit arithmetic,
// price selection, token pre-estimation and the single amount rounding point.
//
// Every monetary value is an int64 count of micro-credits (1 credit = 10,000
// micro-credits; 1 yuan = 100 credits = 1,000,000 micro-credits). The package
// never imports HTTP, Redis or the gateway, so it can be tested without
// infrastructure.
package billing

import "fmt"

// Monetary constants. They are the only conversion factors the package uses.
const (
	// MicroPerCredit is 1 credit expressed in micro-credits.
	MicroPerCredit int64 = 10000
	// MicroPerFen is 1 fen (0.01 yuan) expressed in micro-credits.
	MicroPerFen int64 = 10000
	// MicroPerYuan is 1 yuan expressed in micro-credits.
	MicroPerYuan int64 = 1000000
	// CreditsPerYuan is the platform-wide conversion rate: 1 yuan = 100 credits.
	CreditsPerYuan int64 = 100
	// BasisPoints is 100% in integer basis points.
	BasisPoints int64 = 10000
	// LangCoefBP is the V1.0 language coefficient, fixed at 1.0. It is kept as
	// an integer basis-point value so no float ever enters the estimate path.
	LangCoefBP int64 = 10000
)

// RoundUpDiv returns ceil(n/d) for non-negative n and positive d. It is the
// only integer ceiling helper in the package; callers must not invent their
// own rounding.
func RoundUpDiv(n, d int64) int64 {
	if d <= 0 || n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

// CreditsToMicro converts whole credits to micro-credits.
func CreditsToMicro(credits int64) int64 { return credits * MicroPerCredit }

// MicroToCredits converts micro-credits to whole credits, truncating. Display
// only: financial code must keep micro-credits.
func MicroToCredits(micro int64) int64 { return micro / MicroPerCredit }

// YuanToMicro converts whole yuan to micro-credits.
func YuanToMicro(yuan int64) int64 { return yuan * MicroPerYuan }

// FenToMicro converts fen (payment amounts) to micro-credits.
func FenToMicro(fen int64) int64 { return fen * MicroPerFen }

// MicroToFen converts micro-credits to fen, truncating. One credit is one fen,
// so this is the bridge between the ledger's _micro unit and payment's _fen.
func MicroToFen(micro int64) int64 { return micro / MicroPerFen }

// MicroToYuan converts micro-credits to whole yuan, truncating. Display only.
func MicroToYuan(micro int64) int64 { return micro / MicroPerYuan }

// FormatCredits renders micro-credits as credits with four fractional digits, without
// using floating point.
func FormatCredits(micro int64) string {
	sign, abs := splitSign(micro)
	whole := abs / MicroPerCredit
	frac := abs % MicroPerCredit
	return fmt.Sprintf("%s%d.%04d", sign, whole, frac)
}

// FormatYuan renders micro-credits as yuan with two fractional digits, rounding
// half-up at the fen boundary. Display only; never feed the result back into
// a financial calculation.
func FormatYuan(micro int64) string {
	sign, abs := splitSign(micro)
	fen := (abs + MicroPerFen/2) / MicroPerFen
	yuan := fen / 100
	cents := fen % 100
	return fmt.Sprintf("%s%d.%02d", sign, yuan, cents)
}

func splitSign(v int64) (string, int64) {
	if v < 0 {
		return "-", -v
	}
	return "", v
}
