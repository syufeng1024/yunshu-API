package config

import (
	"reflect"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// TestKeyCount is acceptance A2: the metadata table, the public key list and
// the 0018 seed all contain exactly 45 keys.
func TestKeyCount(t *testing.T) {
	resetState(t)
	if KeyCount != 45 {
		t.Fatalf("KeyCount = %d, want 45", KeyCount)
	}
	if len(keyTable) != KeyCount {
		t.Fatalf("keyTable has %d entries, want %d", len(keyTable), KeyCount)
	}
	if len(AllKeys()) != KeyCount {
		t.Fatalf("AllKeys has %d entries, want %d", len(AllKeys()), KeyCount)
	}
	var dbCount int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM t_config_item`).Scan(&dbCount); err != nil {
		t.Fatalf("count t_config_item: %v", err)
	}
	if dbCount != KeyCount {
		t.Fatalf("t_config_item has %d rows, want %d", dbCount, KeyCount)
	}
}

func TestNamespaceDistribution(t *testing.T) {
	want := map[Namespace]int{
		NsRouting:    0,
		NsRateLimit:  6,
		NsPricing:    21,
		NsUpstream:   9,
		NsFeature:    8,
		NsUnassigned: 1,
	}
	total := 0
	for _, ns := range []Namespace{NsRouting, NsRateLimit, NsPricing, NsUpstream, NsFeature, NsUnassigned} {
		got := len(KeysInNamespace(ns))
		if got != want[ns] {
			t.Errorf("namespace %s has %d keys, want %d", ns, got, want[ns])
		}
		total += got
	}
	if total != KeyCount {
		t.Fatalf("namespace distribution sums to %d, want %d", total, KeyCount)
	}
}

// TestRuntimeWhitelist is the metadata half of acceptance A5. The integration
// half writes all 12 keys through RuntimeWriter.
func TestRuntimeWhitelist(t *testing.T) {
	want := map[Key]bool{
		KeyBillingDeductPolicy:    true,
		KeyBillingPreholdMaxRatio: true,
		KeyGwUpstreamTimeoutMs:    true,
		KeyGwClientTimeoutBudget:  true,
		KeyGwMaxInflight:          true,
		KeyGwDrainDeadlineMs:      true,
		KeyLogRetentionDays:       true,
		KeyQuotaDailyMeter:        true,
		KeyQuotaKeyDailyMode:      true,
		KeyQuotaResetScope:        true,
		KeyQuotaResetTZ:           true,
		KeyQuotaRateLimitOrder:    true,
	}
	got := RuntimeWritableKeys()
	if RuntimeKeyCount != 12 {
		t.Fatalf("RuntimeKeyCount = %d, want 12", RuntimeKeyCount)
	}
	if len(got) != 12 {
		t.Fatalf("runtime whitelist has %d keys, want 12", len(got))
	}
	for _, key := range got {
		if !want[key] {
			t.Errorf("%s is runtime writable but not in the documented whitelist", key)
		}
		if !metaByKey[key].RuntimeWritable {
			t.Errorf("%s is in RuntimeWritableKeys but metadata disagrees", key)
		}
	}
	for key := range want {
		if !RuntimeWritable(key) {
			t.Errorf("%s is missing from the runtime whitelist", key)
		}
	}

	forbidden := []Key{
		KeyGwRetryMax,
		KeyPricingCacheBillingEnabled,
		KeyPricingMinMarginBP,
		KeyPricingPlanMinMarginBP,
		KeyPricingGroupSwitchMode,
		KeyBillingLongCtxGuardBP,
		KeyQuotaOverdraftWarnDays,
	}
	for _, key := range forbidden {
		if RuntimeWritable(key) {
			t.Errorf("%s must not be runtime writable", key)
		}
	}
}

func TestUnassignedKeyNeverEntersSnapshot(t *testing.T) {
	meta, ok := Meta(KeyQuotaOverdraftWarnDays)
	if !ok {
		t.Fatalf("%s metadata missing", KeyQuotaOverdraftWarnDays)
	}
	if meta.Namespace != NsUnassigned {
		t.Fatalf("namespace = %s, want UNASSIGNED", meta.Namespace)
	}
	if namespaceIndex(NsUnassigned) >= 0 {
		t.Fatal("UNASSIGNED must not map to a snapshot slot")
	}
	store := NewStore()
	for _, ns := range SnapshotNamespaces {
		if store.Snapshot(ns).Has(KeyQuotaOverdraftWarnDays) {
			t.Fatalf("%s snapshot contains the unassigned key", ns)
		}
	}
	if _, ok := store.Value(KeyQuotaOverdraftWarnDays); ok {
		t.Fatal("Store.Value returned the unassigned key")
	}
	if snap := newSnapshot(NsUnassigned, 1, time.Time{}); snap.Has(KeyQuotaOverdraftWarnDays) {
		t.Fatal("newSnapshot populated the unassigned bucket")
	}
}

// TestMetadataMatchesSeed is acceptance A2's other half: every database key
// exists in key.go with the same namespace and the same default value, and
// vice versa.
func TestMetadataMatchesSeed(t *testing.T) {
	resetState(t)
	rows, err := testDB.Query(`
		SELECT cfg_namespace, cfg_key, cfg_value
		  FROM t_config_item
		 WHERE scope = 'GLOBAL' AND scope_id = 0
		 ORDER BY cfg_key`)
	if err != nil {
		t.Fatalf("query seed: %v", err)
	}
	defer rows.Close()

	seen := make(map[Key]bool, KeyCount)
	for rows.Next() {
		var namespace, key, value string
		if err := rows.Scan(&namespace, &key, &value); err != nil {
			t.Fatalf("scan seed: %v", err)
		}
		meta, ok := Meta(Key(key))
		if !ok {
			t.Errorf("database key %q has no metadata entry", key)
			continue
		}
		if meta.Namespace != Namespace(namespace) {
			t.Errorf("%s namespace = %s in database, metadata says %s", key, namespace, meta.Namespace)
		}
		defaults, err := Parse(meta.Key, jsonx.RawMessage(meta.Default))
		if err != nil {
			t.Errorf("%s default does not parse: %v", key, err)
			continue
		}
		fromDB, err := Parse(meta.Key, jsonx.RawMessage(value))
		if err != nil {
			t.Errorf("%s seed value %s does not parse: %v", key, value, err)
			continue
		}
		if !sameValue(defaults, fromDB) {
			t.Errorf("%s default = %s, seed = %s", key, defaults.Raw(), fromDB.Raw())
		}
		seen[meta.Key] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate seed: %v", err)
	}
	for _, key := range AllKeys() {
		if !seen[key] {
			t.Errorf("metadata key %s has no t_config_item seed row", key)
		}
	}
}

func TestStrategyBindings(t *testing.T) {
	want := map[Key]StrategyKind{
		KeyBillingDeductPolicy:    KindDeductPolicy,
		KeyBillingRefundPolicy:    KindRefundPolicy,
		KeyPricingCostSource:      KindCostSource,
		KeyQuotaDailyMeter:        KindDailyMeter,
		KeyQuotaKeyDailyMode:      KindKeyDailyQuota,
		KeyQuotaResetScope:        KindResetScope,
		KeyPricingTierChain:       KindTierChain,
		KeyPricingGroupSwitchMode: KindGroupSwitchMode,
	}
	count := 0
	for _, key := range AllKeys() {
		meta := metaByKey[key]
		if meta.Strategy == "" {
			continue
		}
		count++
		if want[key] != meta.Strategy {
			t.Errorf("%s strategy = %s, want %s", key, meta.Strategy, want[key])
		}
	}
	if count != len(want) {
		t.Fatalf("found %d strategy-bound keys, want %d", count, len(want))
	}
}

func sameValue(a, b Value) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case TypeInt:
		return a.intVal == b.intVal
	case TypeBool:
		return a.boolVal == b.boolVal
	case TypeEnum, TypeString:
		return a.strVal == b.strVal
	case TypeDecimal:
		return a.decVal == b.decVal
	case TypeJSON:
		var left, right any
		if err := jsonx.Unmarshal(a.raw, &left); err != nil {
			return false
		}
		if err := jsonx.Unmarshal(b.raw, &right); err != nil {
			return false
		}
		return reflect.DeepEqual(left, right)
	default:
		return false
	}
}
