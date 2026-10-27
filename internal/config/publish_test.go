package config

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
)

// nextVersionFromDB is the version a publish reserves: one above the durable
// max of t_config_item and t_config_publish. The seed rows are version 1 on a
// fresh schema, but yunshu_test keeps publish rows from earlier runs, so the
// baseline is read rather than assumed.
func nextVersionFromDB(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	base, err := readDBVersion(ctx, testDB)
	if err != nil {
		t.Fatalf("read baseline version: %v", err)
	}
	return base + 1
}

// TestPublishUpdatesAllStores verifies the fixed release order end to end:
// t_config_item, t_config_publish, five Redis snapshots, cfg:ver, Pub/Sub.
func TestPublishUpdatesAllStores(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	wantVersion := nextVersionFromDB(t, ctx)

	sub, err := testRedisIface.Subscribe(ctx, RedisInvalidateChannel)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	pub := newTestPublisher()
	result, err := pub.Publish(ctx, []Change{
		{Key: KeyGwMaxInflight, Raw: jsonx.RawMessage(`8100`)},
	}, 0)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	// The publish reserves exactly one above the durable baseline.
	if result.Version != wantVersion {
		t.Fatalf("published version = %d, want %d", result.Version, wantVersion)
	}
	if got := redisString(t, RedisVersionKey); got != strconv.FormatInt(wantVersion, 10) {
		t.Fatalf("cfg:ver = %q, want %d", got, wantVersion)
	}

	var value string
	var itemVersion int64
	if err := testDB.QueryRow(`
		SELECT cfg_value, version FROM t_config_item
		 WHERE cfg_key = ? AND scope = 'GLOBAL' AND scope_id = 0`,
		string(KeyGwMaxInflight)).Scan(&value, &itemVersion); err != nil {
		t.Fatalf("read config item: %v", err)
	}
	parsed, err := Parse(KeyGwMaxInflight, jsonx.RawMessage(value))
	if err != nil {
		t.Fatalf("parse stored value: %v", err)
	}
	if got, _ := parsed.Int(); got != 8100 || itemVersion != wantVersion {
		t.Fatalf("stored value/version = %d/%d, want 8100/%d", got, itemVersion, wantVersion)
	}

	var scope, changed string
	var publishVersion int64
	if err := testDB.QueryRow(`
		SELECT scope, changed_keys, version FROM t_config_publish
		 WHERE publish_id = ?`, result.PublishID).Scan(&scope, &changed, &publishVersion); err != nil {
		t.Fatalf("read publish record: %v", err)
	}
	if scope != string(NsUpstream) || publishVersion != wantVersion || !strings.Contains(changed, string(KeyGwMaxInflight)) {
		t.Fatalf("publish record = scope %q version %d keys %s", scope, publishVersion, changed)
	}

	for _, ns := range SnapshotNamespaces {
		raw := redisString(t, SnapshotRedisKey(ns))
		var wire wireSnapshot
		if err := jsonx.Unmarshal([]byte(raw), &wire); err != nil {
			t.Fatalf("decode %s snapshot: %v", ns, err)
		}
		if wire.Version != wantVersion {
			t.Fatalf("%s snapshot version = %d, want %d", ns, wire.Version, wantVersion)
		}
		if raw, ok := wire.Values[string(KeyGwMaxInflight)]; ok && ns == NsUpstream {
			if string(raw) != "8100" {
				t.Fatalf("UPSTREAM snapshot gw.max_inflight = %s", raw)
			}
		}
	}
	if ttl := testRedis.TTL(ctx, SnapshotRedisKey(NsUpstream)).Val(); ttl <= 0 || ttl > SnapshotTTL {
		t.Fatalf("snapshot TTL = %s, want (0, %s]", ttl, SnapshotTTL)
	}

	select {
	case msg := <-sub.Messages():
		var invalidate invalidateMessage
		if err := jsonx.Unmarshal([]byte(msg), &invalidate); err != nil {
			t.Fatalf("decode invalidate message: %v", err)
		}
		if invalidate.Version != wantVersion || invalidate.PublishID != result.PublishID {
			t.Fatalf("invalidate message = %+v", invalidate)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cfg:invalidate message")
	}

	for ns, size := range result.SnapshotBytes {
		if size <= 0 || size >= MaxSnapshotBytes {
			t.Errorf("%s snapshot size = %d", ns, size)
		}
	}
}

// TestRuntimeWriterAllTwelveKeys is acceptance A5's integration half: every
// whitelisted key can be written through RuntimeWriter and is visible in the
// next snapshot.
func TestRuntimeWriterAllTwelveKeys(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	writer := &RuntimeWriter{Publisher: newTestPublisher()}

	version := nextVersionFromDB(t, ctx) - 1
	for key, raw := range runtimeTestValues {
		result, err := writer.Set(ctx, key, jsonx.RawMessage(raw), 0)
		if err != nil {
			t.Fatalf("write %s=%s: %v", key, raw, err)
		}
		version++
		if result.Version != version {
			t.Fatalf("%s published version = %d, want %d", key, result.Version, version)
		}
	}
	if got := redisString(t, RedisVersionKey); got != strconv.FormatInt(version, 10) {
		t.Fatalf("cfg:ver = %q, want %d", got, version)
	}

	loader := newTestLoader(nil)
	if err := loader.RefreshAll(ctx); err != nil {
		t.Fatalf("refresh after writes: %v", err)
	}
	for key, raw := range runtimeTestValues {
		var stored string
		if err := testDB.QueryRow(`
			SELECT cfg_value FROM t_config_item
			 WHERE cfg_key = ? AND scope = 'GLOBAL' AND scope_id = 0`, string(key)).Scan(&stored); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		want, _ := Parse(key, jsonx.RawMessage(raw))
		got, err := Parse(key, jsonx.RawMessage(stored))
		if err != nil {
			t.Fatalf("parse stored %s: %v", key, err)
		}
		if !sameValue(want, got) {
			t.Errorf("%s stored = %s, want %s", key, got.Raw(), want.Raw())
		}

		meta := metaByKey[key]
		snap := loader.Store.Snapshot(meta.Namespace)
		snapshotValue, ok := snap.Value(key)
		if !ok {
			t.Fatalf("%s missing from %s snapshot", key, meta.Namespace)
		}
		if !sameValue(want, snapshotValue) {
			t.Errorf("%s snapshot = %s, want %s", key, snapshotValue.Raw(), want.Raw())
		}
	}
}

func TestRuntimeWriterRejectsNonWhitelisted(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	writer := &RuntimeWriter{Publisher: newTestPublisher()}

	cases := []Change{
		{Key: KeyQuotaOverdraftWarnDays, Raw: jsonx.RawMessage(`7`)},
		{Key: KeyPricingCacheBillingEnabled, Raw: jsonx.RawMessage(`true`)},
		{Key: KeyGwRetryMax, Raw: jsonx.RawMessage(`1`)},
		{Key: KeyPricingMinMarginBP, Raw: jsonx.RawMessage(`2500`)},
	}
	for _, tc := range cases {
		if _, err := writer.Set(ctx, tc.Key, tc.Raw, 0); !errors.Is(err, ErrConfigValueInvalid) {
			t.Errorf("%s was not rejected with config_value_invalid: %v", tc.Key, err)
		}
	}
	if _, err := testRedisIface.Get(ctx, RedisVersionKey); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("a rejected write advanced cfg:ver: %v", err)
	}
	var publishes int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM t_config_publish WHERE id > ?`, originalPublishMax).Scan(&publishes); err != nil {
		t.Fatalf("count publishes: %v", err)
	}
	if publishes != 0 {
		t.Fatalf("rejected writes created %d publish rows", publishes)
	}
}

func TestPublishRejectsInvalidChanges(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	pub := newTestPublisher()

	cases := []struct {
		name    string
		changes []Change
		want    string
	}{
		{"empty", nil, "at least one"},
		{"unknown key", []Change{{Key: Key("does.not.exist"), Raw: jsonx.RawMessage(`1`)}}, "unknown config key"},
		{"unassigned key", []Change{{Key: KeyQuotaOverdraftWarnDays, Raw: jsonx.RawMessage(`9`)}}, "not assigned"},
		{"duplicate", []Change{
			{Key: KeyGwMaxInflight, Raw: jsonx.RawMessage(`8001`)},
			{Key: KeyGwMaxInflight, Raw: jsonx.RawMessage(`8002`)},
		}, "duplicate"},
		{"bad enum", []Change{{Key: KeyBillingDeductPolicy, Raw: jsonx.RawMessage(`"xxx"`)}}, "outside domain"},
		{"unregistered strategy", []Change{{Key: KeyBillingDeductPolicy, Raw: jsonx.RawMessage(`"prorata"`)}}, EnumFifoExpiry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pub.Publish(ctx, tc.changes, 0)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestPublishRawRejectsOversize is acceptance A8.
func TestPublishRawRejectsOversize(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	pub := newTestPublisher()

	huge := `{"namespace":"ROUTING","version":0,"values":{"routes":"` +
		strings.Repeat("a", 300*1024) + `"}}`
	_, err := pub.PublishRaw(ctx, NsRouting, []byte(huge), nil, 0)
	if err == nil {
		t.Fatal("oversize ROUTING snapshot was accepted")
	}
	if !strings.Contains(err.Error(), "limit is 262144") || !strings.Contains(err.Error(), "snapshot is") {
		t.Fatalf("error lacks actual size/limit: %v", err)
	}
	if _, err := testRedisIface.Get(ctx, RedisVersionKey); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("oversize publish advanced cfg:ver: %v", err)
	}
	var publishes int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM t_config_publish WHERE id > ?`, originalPublishMax).Scan(&publishes); err != nil {
		t.Fatalf("count publishes: %v", err)
	}
	if publishes != 0 {
		t.Fatalf("oversize publish created %d publish rows", publishes)
	}
}

func TestPublishRawRouting(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	pub := newTestPublisher()
	wantVersion := nextVersionFromDB(t, ctx)
	var itemBase int64
	if err := testDB.QueryRow(`SELECT COALESCE(MAX(version),0) FROM t_config_item`).Scan(&itemBase); err != nil {
		t.Fatalf("item baseline: %v", err)
	}

	payload := []byte(`{"namespace":"ROUTING","version":0,"values":{` +
		`"groups":[{"code":"standard","ratio_bp":10000}],` +
		`"routes":[{"model_id":1,"channel_id":1}]}}`)
	result, err := pub.PublishRaw(ctx, NsRouting, payload, nil, 0)
	if err != nil {
		t.Fatalf("publish raw: %v", err)
	}
	if result.Version != wantVersion {
		t.Fatalf("raw publish version = %d, want %d", result.Version, wantVersion)
	}
	raw := redisString(t, SnapshotRedisKey(NsRouting))
	snap, err := decodeSnapshot(NsRouting, []byte(raw), wantVersion, time.Now())
	if err != nil {
		t.Fatalf("decode routing snapshot: %v", err)
	}
	if _, ok := snap.Extra("groups"); !ok {
		t.Fatal("routing snapshot lost the groups member")
	}
	if _, ok := snap.Extra("routes"); !ok {
		t.Fatal("routing snapshot lost the routes member")
	}

	var itemVersion, publishVersion int64
	if err := testDB.QueryRow(`SELECT COALESCE(MAX(version),0) FROM t_config_item`).Scan(&itemVersion); err != nil {
		t.Fatalf("item version: %v", err)
	}
	if err := testDB.QueryRow(`SELECT COALESCE(MAX(version),0) FROM t_config_publish`).Scan(&publishVersion); err != nil {
		t.Fatalf("publish version: %v", err)
	}
	if itemVersion != itemBase || publishVersion != wantVersion {
		t.Fatalf("versions item=%d publish=%d, want %d/%d (raw does not touch items)",
			itemVersion, publishVersion, itemBase, wantVersion)
	}

	// The next key publish must reserve from the publish record, not from the
	// untouched item rows.
	next, err := pub.Publish(ctx, []Change{
		{Key: KeyGwMaxInflight, Raw: jsonx.RawMessage(`8200`)},
	}, 0)
	if err != nil {
		t.Fatalf("publish after raw: %v", err)
	}
	if next.Version != wantVersion+1 {
		t.Fatalf("version after raw publish = %d, want %d", next.Version, wantVersion+1)
	}
}

func TestPriceCacheStoresStandardPriceOnly(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	cache := &PriceCache{Redis: testRedisIface}

	standard := jsonx.RawMessage(`{"model_id":42,"in_price_micro_per_k":8000,"out_price_micro_per_k":20000}`)
	if err := cache.SetStandard(ctx, 42, standard, 0); err != nil {
		t.Fatalf("set standard price: %v", err)
	}
	got, err := cache.GetStandard(ctx, 42)
	if err != nil {
		t.Fatalf("get standard price: %v", err)
	}
	if string(got) != string(standard) {
		t.Fatalf("cached value changed: %s", got)
	}
	if ttl := testRedis.TTL(ctx, PriceCacheKey(42)).Val(); ttl <= 0 || ttl > PriceCacheTTL {
		t.Fatalf("price cache TTL = %s, want (0, %s]", ttl, PriceCacheTTL)
	}
	if _, err := cache.GetStandard(ctx, 43); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("missing price returned %v, want ErrKeyNotFound", err)
	}
	if err := cache.SetStandard(ctx, 42, nil, 0); err == nil {
		t.Fatal("empty standard price was accepted")
	}
}
