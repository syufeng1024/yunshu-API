package config

import "testing"

// BenchmarkStoreInt is acceptance A9's benchmark: the read path is one
// atomic.Pointer load plus one map lookup, with no mutex and no allocation.
func BenchmarkStoreInt(b *testing.B) {
	store := NewStore()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Int(KeyGwMaxInflight)
	}
}

func BenchmarkSnapshotDecimal(b *testing.B) {
	snap := NewStore().Snapshot(NsPricing)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = snap.Decimal(KeyPricingTaxRateMultiplier)
	}
}
