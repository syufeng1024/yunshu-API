package billing

import "testing"

// A9: std=1000, ratio=8500, cost=600 => actual 850, required 800, pass.
func TestA9FloorPass(t *testing.T) {
	cost := int64(600)
	got := CheckFloorUnit(1000, 8500, &cost, 2500)
	if !got.OK || got.Skipped || got.MissingCost || got.Invalid {
		t.Fatalf("result = %+v, want OK", got)
	}
	if got.ActualUnitMicro != 850 {
		t.Fatalf("actual = %d, want 850", got.ActualUnitMicro)
	}
	if got.RequiredUnitMicro != 800 {
		t.Fatalf("required = %d, want 800", got.RequiredUnitMicro)
	}
	if got.ShortfallMicro != 0 {
		t.Fatalf("shortfall = %d, want 0", got.ShortfallMicro)
	}
	t.Logf("floor pass: std=1000 ratio=8500 actual=%d cost=600 required=%d shortfall=%d",
		got.ActualUnitMicro, got.RequiredUnitMicro, got.ShortfallMicro)
}

// A9: std=1000, ratio=8500, cost=800 => actual 850, required 1067, reject
// with a 217 micro/1k shortfall.
func TestA9FloorRejectWithShortfall(t *testing.T) {
	cost := int64(800)
	got := CheckFloorUnit(1000, 8500, &cost, 2500)
	if got.OK {
		t.Fatalf("result = %+v, want rejection", got)
	}
	if got.ActualUnitMicro != 850 {
		t.Fatalf("actual = %d, want 850", got.ActualUnitMicro)
	}
	if got.RequiredUnitMicro != 1067 {
		t.Fatalf("required = %d, want 1067", got.RequiredUnitMicro)
	}
	if got.ShortfallMicro != 217 {
		t.Fatalf("shortfall = %d, want 217", got.ShortfallMicro)
	}
	t.Logf("floor reject: std=1000 ratio=8500 actual=%d cost=800 required=%d shortfall=%d",
		got.ActualUnitMicro, got.RequiredUnitMicro, got.ShortfallMicro)
}

// G-06: 30,000 × 0.6 = 18,000 < 20,000 × 4/3 = 26,667 => reject.
func TestG06ContractRatioRejected(t *testing.T) {
	cost := int64(20000)
	got := CheckFloorUnit(30000, 6000, &cost, 2500)
	if got.OK {
		t.Fatalf("result = %+v, want rejection", got)
	}
	if got.RequiredUnitMicro != 26667 {
		t.Fatalf("required = %d, want 26667 (integer 4/3, not 26660)", got.RequiredUnitMicro)
	}
	if got.ShortfallMicro != 8667 {
		t.Fatalf("shortfall = %d, want 8667", got.ShortfallMicro)
	}
}

// The exact boundary: 26,667 passes, 26,666 fails. An implementation using
// 1.333 would flip this boundary.
func TestG06ExactBoundary(t *testing.T) {
	cost := int64(20000)
	pass := CheckFloorUnit(26667, 10000, &cost, 2500)
	if !pass.OK {
		t.Fatalf("26,667 should pass: %+v", pass)
	}
	fail := CheckFloorUnit(26666, 10000, &cost, 2500)
	if fail.OK {
		t.Fatalf("26,666 should fail: %+v", fail)
	}
}

func TestInternalGroupSkipsFloor(t *testing.T) {
	cost := int64(800)
	got := CheckFloorUnit(1000, 0, &cost, 2500)
	if !got.OK || !got.Skipped {
		t.Fatalf("internal group result = %+v, want OK+Skipped", got)
	}
}

func TestMissingCostIsNotZero(t *testing.T) {
	got := CheckFloorUnit(1000, 8500, nil, 2500)
	if got.OK || !got.MissingCost {
		t.Fatalf("missing cost result = %+v, want OK=false + MissingCost", got)
	}
	if got.ActualUnitMicro != 850 {
		t.Fatalf("actual = %d, want 850", got.ActualUnitMicro)
	}
}

func TestInvalidRatioRejected(t *testing.T) {
	cost := int64(100)
	got := CheckFloorUnit(1000, 10001, &cost, 2500)
	if !got.Invalid || got.OK {
		t.Fatalf("ratio > 10000 result = %+v, want Invalid", got)
	}
}

func TestMarginBPReported(t *testing.T) {
	cost := int64(600)
	got := CheckFloorUnit(1000, 8500, &cost, 2500)
	// (850-600)/850 = 29.41% => 2941 bp.
	if got.MarginBP != 2941 {
		t.Fatalf("margin = %d bp, want 2941", got.MarginBP)
	}
}
