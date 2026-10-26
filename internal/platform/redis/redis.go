// Package redis configures the shared Redis client used by the three processes.
package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	dialTimeout  = 2 * time.Second
	readTimeout  = 2 * time.Second
	writeTimeout = 2 * time.Second
)

// Open returns a client. Connections are established lazily; readiness owns
// the connectivity check.
func Open(addr, password string, poolSize int) *goredis.Client {
	if poolSize <= 0 {
		poolSize = 20
	}
	return goredis.NewClient(&goredis.Options{
		Addr:         addr,
		Password:     password,
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		PoolSize:     poolSize,
		MinIdleConns: 2,
		MaxRetries:   1,
	})
}

// Ping performs the readiness probe with the 200ms budget.
func Ping(ctx context.Context, c *goredis.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	return c.Ping(ctx).Err()
}
