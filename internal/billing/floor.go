package billing

// FloorResult is the outcome of one margin-floor check. OK=false with
// MissingCost=true is not the same as a margin failure: it means the cost
// price is absent and the caller must downgrade to "warn + explicit
// confirmation" (PRD G-06), never treat the missing cost as zero.
type FloorResult struct {
	OK          bool
	Skipped     bool
	MissingCost bool
	Invalid     bool

	ActualUnitMicro   int64
	RequiredUnitMicro int64
	ShortfallMicro    int64
	MarginBP          int64
}

// CheckFloorUnit checks
//
//	standard_unit_price × ratio_bp / 10000 >= cost_unit_price × 10000 / (10000 - min_margin_bp)
//
// using only integer cross multiplication:
//
//	std * ratio_bp * (10000 - min_margin_bp) >= cost * 10000 * 10000
//
// Derivation: the required unit price is cost × 10000 ÷ 7500 when
// min_margin_bp = 2500. Multiplying both sides of
//
//	std × ratio_bp / 10000 >= cost × 10000 / 7500
//
// by 10000 × 7500 gives the inequality above. No ratio is computed first, so
// the boundary is exact (G-06: 20,000 × 4 ÷ 3 = 26,667, not 26,660).
//
// ratio_bp == 0 is the internal group: it does not participate in the floor
// check at all (PRD §3.5, F16-5 four-constraint fallback lives in T15).
func CheckFloorUnit(
	stdUnitMicro, ratioBP int64,
	costUnitMicro *int64,
	minMarginBP int64,
) FloorResult {
	res := FloorResult{ActualUnitMicro: stdUnitMicro * ratioBP / BasisPoints}

	if ratioBP < 0 || ratioBP > BasisPoints {
		res.Invalid = true
		return res
	}
	if ratioBP == 0 {
		res.OK = true
		res.Skipped = true
		return res
	}
	if minMarginBP < 0 || minMarginBP >= BasisPoints {
		res.Invalid = true
		return res
	}
	if costUnitMicro == nil {
		res.MissingCost = true
		return res
	}
	if stdUnitMicro < 0 || *costUnitMicro < 0 {
		res.Invalid = true
		return res
	}

	denominator := BasisPoints - minMarginBP // 7500 for the 25% floor
	lhs := stdUnitMicro * ratioBP * denominator
	rhs := *costUnitMicro * BasisPoints * BasisPoints

	res.RequiredUnitMicro = RoundUpDiv(*costUnitMicro*BasisPoints, denominator)
	if res.RequiredUnitMicro > res.ActualUnitMicro {
		res.ShortfallMicro = res.RequiredUnitMicro - res.ActualUnitMicro
	}
	if res.ActualUnitMicro > 0 {
		res.MarginBP = (res.ActualUnitMicro - *costUnitMicro) * BasisPoints / res.ActualUnitMicro
	}
	res.OK = lhs >= rhs
	return res
}
