package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// TestVersionSkewNewVersionOldSnapshot is acceptance A6. cfg:ver says 2 while
// every snapshot still embeds version 1. The loader must retry three times,
// return the skew error (not swallow it), log WARN, and keep the old pointer.
func TestVersionSkewNewVersionOldSnapshot(t *testing.T) {
	resetState(t)
	logger := &captureLogger{}
	loader := newTestLoader(logger)
	ctx := testCtx(t)

	writeSnapshotsAt(t, 1)
	if err := loader.RefreshAll(ctx); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	old := loader.Store.Snapshot(NsPricing)
	if old.Version() != 1 {
		t.Fatalf("initial PRICING version = %d, want 1", old.Version())
	}

	// New version number, old snapshot payload. Both cfg:ver reads return 2,
	// so only the embedded payload version can detect the mismatch.
	if err := testRedisIface.Set(ctx, RedisVersionKey, "2", 0); err != nil {
		t.Fatalf("set cfg:ver: %v", err)
	}

	err := loader.RefreshNamespace(ctx, NsPricing)
	if err == nil {
		t.Fatal("RefreshNamespace accepted a new version with an old snapshot")
	}
	var skew *VersionSkewError
	if !errors.As(err, &skew) {
		t.Fatalf("error %T is not *VersionSkewError: %v", err, err)
	}
	if skew.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", skew.Attempts)
	}
	if skew.Snapshot != 1 {
		t.Fatalf("snapshot version in error = %d, want 1", skew.Snapshot)
	}
	if !strings.Contains(err.Error(), "version skew") {
		t.Fatalf("error does not describe the skew: %v", err)
	}
	if loader.Store.Snapshot(NsPricing) != old {
		t.Fatal("store pointer changed despite the failed refresh")
	}
	if !logger.contains("config_version_skew") {
		t.Fatalf("no WARN logged; messages: %v", logger.messages())
	}
}

func TestDecodeSnapshotFallsBackPerField(t *testing.T) {
	payload, err := jsonx.Marshal(wireSnapshot{
		Namespace: string(NsPricing),
		Version:   7,
		Values: map[string]jsonx.RawMessage{
			string(KeyGwMaxInflight):                jsonx.RawMessage(`9999`),
			"unknown.key":                           jsonx.RawMessage(`1`),
			string(KeyBillingDeductPolicy):          jsonx.RawMessage(`"prorata"`),
			string(KeyBillingOverdraftLimitDefault): jsonx.RawMessage(`100`),
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	snap, err := decodeSnapshot(NsPricing, payload, 7, time.Now())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// A registered override is applied.
	if got, _ := snap.Int(KeyBillingOverdraftLimitDefault); got != 100 {
		t.Fatalf("overdraft limit = %d, want 100", got)
	}
	// An enum value with no registered implementation falls back to default.
	if got, _ := snap.String(KeyBillingDeductPolicy); got != EnumFifoExpiry {
		t.Fatalf("deduct policy = %q, want %q", got, EnumFifoExpiry)
	}
	// A key from another namespace and an unknown key are ignored.
	if snap.Has(KeyGwMaxInflight) {
		t.Fatal("PRICING snapshot accepted an UPSTREAM key")
	}
	if snap.Has(Key("unknown.key")) {
		t.Fatal("PRICING snapshot accepted an unknown key")
	}
	if got := len(snap.Fallbacks()); got < 3 {
		t.Fatalf("fallbacks = %v, want at least 3 entries", snap.Fallbacks())
	}
}

func TestDecodeSnapshotKeepsUnassignedOut(t *testing.T) {
	payload, err := jsonx.Marshal(wireSnapshot{
		Namespace: string(NsRateLimit),
		Version:   1,
		Values: map[string]jsonx.RawMessage{
			string(KeyQuotaOverdraftWarnDays): jsonx.RawMessage(`9`),
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	snap, err := decodeSnapshot(NsRateLimit, payload, 1, time.Now())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.Has(KeyQuotaOverdraftWarnDays) {
		t.Fatal("unassigned key entered a RATELIMIT snapshot")
	}
	if !strings.Contains(strings.Join(snap.Fallbacks(), ";"), "another namespace") {
		t.Fatalf("expected a namespace fallback, got %v", snap.Fallbacks())
	}
}

func TestDecodeSnapshotFillsDefaults(t *testing.T) {
	payload, err := jsonx.Marshal(wireSnapshot{
		Namespace: string(NsUpstream),
		Version:   4,
		Values:    map[string]jsonx.RawMessage{},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	snap, err := decodeSnapshot(NsUpstream, payload, 4, time.Now())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := snap.Int(KeyGwMaxInflight); got != 8000 {
		t.Fatalf("%s = %d, want default 8000", KeyGwMaxInflight, got)
	}
	if got, _ := snap.Int(KeyGwRetryMax); got != 1 {
		t.Fatalf("%s = %d, want default 1", KeyGwRetryMax, got)
	}
}

func TestKeylessNamespaceRoundTrip(t *testing.T) {
	payload, err := jsonx.Marshal(wireSnapshot{
		Namespace: string(NsRouting),
		Version:   3,
		Values: map[string]jsonx.RawMessage{
			"groups": jsonx.RawMessage(`[{"code":"standard","ratio_bp":10000}]`),
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	snap, err := decodeSnapshot(NsRouting, payload, 3, time.Now())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, ok := snap.Extra("groups")
	if !ok || !strings.Contains(string(raw), "standard") {
		t.Fatalf("opaque routing value missing: %q ok=%v", raw, ok)
	}
	encoded, err := encodeSnapshot(snap)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	again, err := decodeSnapshot(NsRouting, encoded, 3, time.Now())
	if err != nil {
		t.Fatalf("decode re-encoded: %v", err)
	}
	if _, ok := again.Extra("groups"); !ok {
		t.Fatal("opaque routing value lost in round trip")
	}
}

func TestNewStoreFromDB(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	store, err := NewStoreFromDB(ctx, testDB)
	if err != nil {
		t.Fatalf("NewStoreFromDB: %v", err)
	}
	if got, _ := store.Int(KeyGwMaxInflight); got != 8000 {
		t.Fatalf("%s = %d, want 8000", KeyGwMaxInflight, got)
	}
	if got, _ := store.String(KeyBillingDeductPolicy); got != EnumFifoExpiry {
		t.Fatalf("%s = %q", KeyBillingDeductPolicy, got)
	}
	if store.GlobalVersion() != 1 {
		t.Fatalf("global version = %d, want seed version 1", store.GlobalVersion())
	}
	for _, ns := range SnapshotNamespaces {
		if store.Snapshot(ns).Has(KeyQuotaOverdraftWarnDays) {
			t.Fatalf("%s snapshot contains the unassigned key", ns)
		}
	}
}

func TestSnapshotSizeLimit(t *testing.T) {
	atLimit := make([]byte, MaxSnapshotBytes)
	err := checkSnapshotSize(NsRouting, atLimit)
	if err == nil {
		t.Fatal("a snapshot exactly at the limit was accepted")
	}
	if !strings.Contains(err.Error(), "262144") || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error lacks actual size/limit: %v", err)
	}
	if err := checkSnapshotSize(NsRouting, atLimit[:MaxSnapshotBytes-1]); err != nil {
		t.Fatalf("a snapshot just below the limit was rejected: %v", err)
	}
}

func TestSnapshotGetterSurface(t *testing.T) {
	snap := NewStore().Snapshot(NsPricing)
	if raw, ok := snap.Raw(KeyPricingTaxRateMultiplier); !ok || len(raw) == 0 {
		t.Fatalf("Raw = %q, %v", raw, ok)
	}
	value, ok := snap.Value(KeyPricingTaxRateMultiplier)
	if !ok || value.Kind() != TypeDecimal {
		t.Fatalf("Value kind = %s, ok=%v", value.Kind(), ok)
	}
	decimal, _ := value.Decimal()
	if decimal.BasisPoints() != DecimalScale || decimal.Float64() != 1.0 {
		t.Fatalf("decimal = %d (%v)", decimal.BasisPoints(), decimal.Float64())
	}
	if got, ok := snap.Bool(KeyPricingCacheBillingEnabled); !ok || got {
		t.Fatalf("Bool = %v, %v", got, ok)
	}
	if got, ok := snap.String(KeyPricingCostSource); !ok || got != EnumManual {
		t.Fatalf("String = %q, %v", got, ok)
	}
	if !snap.LoadedAt().IsZero() {
		t.Fatalf("bootstrap LoadedAt = %s, want zero", snap.LoadedAt())
	}
	if names := snap.ExtraNames(); len(names) != 0 {
		t.Fatalf("key-driven snapshot has extras: %v", names)
	}
}

func TestRefreshFailureKeepsOldSnapshot(t *testing.T) {
	resetState(t)
	logger := &captureLogger{}
	loader := newTestLoader(logger)
	ctx := testCtx(t)
	writeSnapshotsAt(t, 1)
	if err := loader.RefreshAll(ctx); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	old := loader.Store.Snapshot(NsPricing)

	// Advance every payload to version 2, then corrupt one frame. The version
	// protocol is satisfied; only the parser can reject the read.
	writeSnapshotsAt(t, 2)
	if err := testRedisIface.Set(ctx, SnapshotRedisKey(NsPricing), "{not-json", 0); err != nil {
		t.Fatalf("corrupt snapshot: %v", err)
	}
	if err := loader.RefreshAll(ctx); err == nil {
		t.Fatal("RefreshAll accepted a corrupt snapshot")
	}
	if loader.Store.Snapshot(NsPricing) != old {
		t.Fatal("store changed after a parse failure")
	}
	if !logger.contains("config_parse_failed") {
		t.Fatalf("parse failure not logged: %v", logger.messages())
	}
}

func TestRefreshRejectsNonNumericVersion(t *testing.T) {
	resetState(t)
	loader := newTestLoader(nil)
	ctx := testCtx(t)
	writeSnapshotsAt(t, 1)
	if err := loader.RefreshAll(ctx); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	old := loader.Store.GlobalVersion()
	if err := testRedisIface.Set(ctx, RedisVersionKey, "not-a-number", 0); err != nil {
		t.Fatalf("set cfg:ver: %v", err)
	}
	if err := loader.RefreshAll(ctx); err == nil {
		t.Fatal("RefreshAll accepted a non-numeric version")
	}
	if loader.Store.GlobalVersion() != old {
		t.Fatal("store version changed after a version read failure")
	}
}

// TestStoreReadNoAllocs is acceptance A9's in-process half: reads take no
// mutex and allocate nothing.
func TestStoreReadNoAllocs(t *testing.T) {
	store := NewStore()
	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = store.Int(KeyGwMaxInflight)
	})
	if allocs != 0 {
		t.Fatalf("Store.Int allocated %v times per call", allocs)
	}
	snap := store.Snapshot(NsPricing)
	allocs = testing.AllocsPerRun(1000, func() {
		_, _ = snap.Decimal(KeyPricingTaxRateMultiplier)
	})
	if allocs != 0 {
		t.Fatalf("Snapshot.Decimal allocated %v times per call", allocs)
	}
}
