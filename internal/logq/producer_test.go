package logq

import (
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/redisx"
	"yunshu.dev/core/internal/redisx/redistest"
)

func TestMain(m *testing.M) { os.Exit(redistest.Setup(m)) }

func newProducer(t *testing.T) (*Producer, *redisx.Client) {
	t.Helper()
	client := redisx.New(redisx.Options{Addr: redistest.Addr(), DB: redistest.DB()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client, 0), client
}

func TestAddWritesVersionAndPayload(t *testing.T) {
	if err := redistest.Client().FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	producer, _ := newProducer(t)
	if err := producer.Add(t.Context(), []byte(`{"request_id":"r1"}`)); err != nil {
		t.Fatal(err)
	}
	if n := redistest.Client().XLen(t.Context(), redisx.LogStreamKey()).Val(); n != 1 {
		t.Fatalf("XLEN = %d want 1", n)
	}
	messages, err := redistest.Client().XRange(t.Context(), redisx.LogStreamKey(), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[0].Values["v"] != EntryVersion {
		t.Fatalf("v = %v want %s", messages[0].Values["v"], EntryVersion)
	}
	if messages[0].Values["p"] != `{"request_id":"r1"}` {
		t.Fatalf("p = %v", messages[0].Values["p"])
	}
	if err := producer.Add(t.Context(), nil); err == nil {
		t.Fatal("empty payload accepted")
	}
}

func TestClaimStaleUsesPendingPlusClaim(t *testing.T) {
	if err := redistest.Client().FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	producer, _ := newProducer(t)
	const (
		group    = "jobsd"
		consumer = "worker-2"
	)
	if err := producer.Add(t.Context(), []byte(`{"request_id":"r2"}`)); err != nil {
		t.Fatal(err)
	}
	raw := redistest.Client()
	if err := raw.XGroupCreate(t.Context(), redisx.LogStreamKey(), group, "0").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.XReadGroup(t.Context(), &goredis.XReadGroupArgs{
		Group: group, Consumer: "worker-1", Streams: []string{redisx.LogStreamKey(), ">"}, Count: 1,
	}).Result(); err != nil {
		t.Fatal(err)
	}
	claimed, err := producer.ClaimStale(t.Context(), group, consumer, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed = %d want 1", len(claimed))
	}
	if claimed[0].Values["p"] != `{"request_id":"r2"}` {
		t.Fatalf("claimed payload = %v", claimed[0].Values["p"])
	}
}
