package config

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// TestWatcherPollsWithoutPubSub is acceptance A7: the subscriber is alive but
// hears nothing (a unique channel), so only the version poll can observe the
// change. The production interval is 30s; the test shortens it to 25ms.
func TestWatcherPollsWithoutPubSub(t *testing.T) {
	resetState(t)
	if DefaultPollInterval != 30*time.Second {
		t.Fatalf("DefaultPollInterval = %s, want 30s", DefaultPollInterval)
	}

	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	writeSnapshotsAt(t, 1)

	logger := &captureLogger{}
	store := NewStore()
	watcher := &Watcher{
		Redis:            testRedisIface,
		Store:            store,
		Logger:           logger,
		PollInterval:     25 * time.Millisecond,
		SubscribeChannel: "cfg:invalidate-test-" + strconv.Itoa(time.Now().Nanosecond()),
	}
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()

	waitFor(t, 3*time.Second, "initial version 1", func() bool {
		return store.GlobalVersion() == 1
	})

	// Nothing is published on the subscription channel.
	writeSnapshotsAt(t, 2)
	waitFor(t, 3*time.Second, "version 2 via poll", func() bool {
		return store.GlobalVersion() == 2
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watcher returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop on cancellation")
	}
}

func TestWatcherReceivesPubSub(t *testing.T) {
	resetState(t)
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	writeSnapshotsAt(t, 1)

	logger := &captureLogger{}
	store := NewStore()
	watcher := &Watcher{
		Redis:        testRedisIface,
		Store:        store,
		Logger:       logger,
		PollInterval: 10 * time.Second, // Only Pub/Sub can explain an update this fast.
	}
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()

	waitFor(t, 3*time.Second, "initial version 1", func() bool {
		return store.GlobalVersion() == 1
	})

	writeSnapshotsAt(t, 2)
	if err := testRedisIface.Publish(ctx, RedisInvalidateChannel, `{"version":2}`); err != nil {
		t.Fatalf("publish invalidate: %v", err)
	}
	waitFor(t, 2*time.Second, "version 2 via Pub/Sub", func() bool {
		return store.GlobalVersion() == 2
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

type failingSubscribeRedis struct {
	Redis
}

func (failingSubscribeRedis) Subscribe(context.Context, string) (Subscription, error) {
	return nil, errors.New("subscription disabled for test")
}

// TestWatcherPollsWhenSubscriptionFails proves that a dead subscription does
// not make the watcher stale or fatal.
func TestWatcherPollsWhenSubscriptionFails(t *testing.T) {
	resetState(t)
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	writeSnapshotsAt(t, 1)

	logger := &captureLogger{}
	store := NewStore()
	watcher := &Watcher{
		Redis:          failingSubscribeRedis{Redis: testRedisIface},
		Store:          store,
		Logger:         logger,
		PollInterval:   25 * time.Millisecond,
		SubscribeRetry: 50 * time.Millisecond,
	}
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()

	waitFor(t, 3*time.Second, "initial version 1", func() bool {
		return store.GlobalVersion() == 1
	})
	writeSnapshotsAt(t, 2)
	waitFor(t, 3*time.Second, "version 2 despite subscribe failure", func() bool {
		return store.GlobalVersion() == 2
	})

	if !logger.contains("config_subscribe_failed") {
		t.Fatalf("subscribe failure was not logged: %v", logger.messages())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestWatcherRequiresRedisAndStore(t *testing.T) {
	if err := (&Watcher{}).Run(context.TODO()); !errors.Is(err, errWatcherConfig) {
		t.Fatalf("Run error = %v, want errWatcherConfig", err)
	}
}
