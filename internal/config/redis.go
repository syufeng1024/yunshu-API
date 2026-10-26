package config

import (
	"context"
	"errors"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Redis key and channel names from TDS §2.1 K8 / K9 / K10.
const (
	RedisVersionKey        = "cfg:ver"
	RedisSnapshotPrefix    = "cfg:snap:"
	RedisInvalidateChannel = "cfg:invalidate"
)

// SnapshotTTL is the K9 lifetime. It is a bound on stale keys, not the
// invalidation mechanism: nodes hold immutable in-process snapshots and the
// 30s version poll is the fallback channel.
const SnapshotTTL = 300 * time.Second

// SnapshotRedisKey returns cfg:snap:{namespace}.
func SnapshotRedisKey(ns Namespace) string { return RedisSnapshotPrefix + string(ns) }

// ErrKeyNotFound is returned by Redis.Get for a missing key. It is distinct
// from a transport error so the loader can tell "not published yet" from
// "Redis is unavailable".
var ErrKeyNotFound = errors.New("config: redis key not found")

// Subscription is an established Pub/Sub subscription.
type Subscription interface {
	Messages() <-chan string
	Close() error
}

// Redis is the narrow surface config needs. The pool is injected; config does
// not own or globally cache a connection.
type Redis interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Incr(ctx context.Context, key string) (int64, error)
	Publish(ctx context.Context, channel, payload string) error
	Subscribe(ctx context.Context, channel string) (Subscription, error)
}

// GoRedis adapts a go-redis client to the Redis interface. Keeping the
// adapter here lets tests inject a counting/silent wrapper without dragging
// go-redis types through the loader.
type GoRedis struct {
	client *goredis.Client
}

// NewGoRedis wraps client.
func NewGoRedis(client *goredis.Client) *GoRedis { return &GoRedis{client: client} }

// Get implements Redis.
func (g *GoRedis) Get(ctx context.Context, key string) (string, error) {
	s, err := g.client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", ErrKeyNotFound
	}
	if err != nil {
		return "", err
	}
	return s, nil
}

// Set implements Redis. A zero ttl means no expiration.
func (g *GoRedis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return g.client.Set(ctx, key, value, ttl).Err()
}

// Incr implements Redis.
func (g *GoRedis) Incr(ctx context.Context, key string) (int64, error) {
	return g.client.Incr(ctx, key).Result()
}

// Publish implements Redis.
func (g *GoRedis) Publish(ctx context.Context, channel, payload string) error {
	return g.client.Publish(ctx, channel, payload).Err()
}

// Subscribe implements Redis. Receive waits for the subscription
// confirmation so a caller cannot mistake a failed SUBSCRIBE for an idle
// channel.
func (g *GoRedis) Subscribe(ctx context.Context, channel string) (Subscription, error) {
	ps := g.client.Subscribe(ctx, channel)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, err
	}
	sub := &goRedisSubscription{
		ps:   ps,
		out:  make(chan string, 16),
		done: make(chan struct{}),
	}
	go sub.pump()
	return sub, nil
}

type goRedisSubscription struct {
	ps   *goredis.PubSub
	out  chan string
	done chan struct{}
	once sync.Once
}

func (s *goRedisSubscription) Messages() <-chan string { return s.out }

func (s *goRedisSubscription) Close() error {
	s.once.Do(func() { close(s.done) })
	return s.ps.Close()
}

func (s *goRedisSubscription) pump() {
	in := s.ps.Channel()
	defer close(s.out)
	for {
		select {
		case <-s.done:
			return
		case msg, ok := <-in:
			if !ok {
				return
			}
			select {
			case s.out <- msg.Payload:
			case <-s.done:
				return
			}
		}
	}
}
