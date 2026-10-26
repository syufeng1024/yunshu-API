package redisx_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/redisx/redistest"
)

func TestMain(m *testing.M) { os.Exit(redistest.Setup(m)) }

func TestCheckPolicyRequiresNoEviction(t *testing.T) {
	client := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB()})
	defer client.Close()
	if err := client.CheckPolicy(t.Context()); err != nil {
		t.Fatalf("the local Redis must be configured with noeviction: %v", err)
	}
}

func TestClockSyncUsesRedisTime(t *testing.T) {
	client := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB()})
	defer client.Close()
	clock := redisx.NewClock(nil, nil)
	offset, err := clock.Sync(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if offset > 5_000 || offset < -5_000 {
		t.Fatalf("offset = %dms; the test host is more than 5s away from Redis", offset)
	}
	corrected := clock.NowMS()
	local := time.Now().UTC().UnixMilli()
	if diff := corrected - local; diff > 5_000 || diff < -5_000 {
		t.Fatalf("corrected now differs from local by %dms", diff)
	}
}

func TestPing(t *testing.T) {
	client := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB()})
	defer client.Close()
	if err := client.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKeyAuthCacheWriteThrough(t *testing.T) {
	if err := redistest.Client().FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	client := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB()})
	defer client.Close()
	cache := redisx.NewKeyAuthCache(client)
	const hash = "deadbeefdeadbeef"

	rec := redisx.KeyAuthRecord{
		UID: 11, KID: 22, ST: "enabled", IPM: 3, EXP: 0,
		NT: "cfg-7", KN: "prod-key", GRP: "standard", GP: 10000,
	}
	if err := cache.Upsert(t.Context(), hash, rec); err != nil {
		t.Fatal(err)
	}
	if ttl := redistest.Client().PTTL(t.Context(), redisx.APIKeyHashKey(hash)).Val(); ttl != -1 {
		t.Fatalf("K13 TTL = %v; K13 must never expire", ttl)
	}
	got, err := cache.Lookup(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if got != rec {
		t.Fatalf("lookup = %+v want %+v", got, rec)
	}

	if err := cache.BulkUpdateGroupRatio(t.Context(), []string{hash}, 8000); err != nil {
		t.Fatal(err)
	}
	got, err = cache.Lookup(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.GP != 8000 || got.KN != "prod-key" || got.UID != 11 {
		t.Fatalf("bulk ratio update damaged other fields: %+v", got)
	}

	if err := cache.Delete(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Lookup(t.Context(), hash); !errors.Is(err, redisx.ErrKeyAuthNotFound) {
		t.Fatalf("lookup after delete = %v want ErrKeyAuthNotFound (401, no DB fallback)", err)
	}
	if err := cache.Upsert(t.Context(), "NOT-HEX", rec); err == nil {
		t.Fatal("invalid hash accepted")
	}
}
