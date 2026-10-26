// Package redisx owns the Redis connection surface and the single definition
// of the 26-key namespace (TDS §2.1).
//
// Redis is never a source of truth for money (GR-02). Losing every key can
// only under-enforce rate limits for a short window; it cannot change the
// ledger in t_ledger_entry. That premise is why this package refuses to start
// against a Redis whose maxmemory-policy can evict counters: an evicted RPM
// counter silently raises the allowance, and an evicted prehold sentinel makes
// a replay look like a fresh request.
package redisx

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Timeouts are the per-command budgets from TDS §4.2. They are deliberately
// shorter than the pipeline stage budget: a slow Redis must fail one request
// fast, not consume the request's whole deadline.
const (
	DefaultCommandTimeout = 50 * time.Millisecond
	DefaultLuaTimeout     = 100 * time.Millisecond
	defaultDialTimeout    = 2 * time.Second
)

// ErrPolicyRejected means the server's maxmemory-policy is not noeviction.
var ErrPolicyRejected = errors.New("redisx: maxmemory-policy must be noeviction")

// Options configures the shared client.
type Options struct {
	Addr     string
	Password string
	DB       int
	PoolSize int

	CommandTimeout time.Duration
	LuaTimeout     time.Duration
	DialTimeout    time.Duration
}

// Client wraps go-redis with the two command budgets every caller must use.
// Callers reach Raw only for commands this package does not wrap; they still
// pass CommandContext or LuaContext.
type Client struct {
	rdb            *goredis.Client
	commandTimeout time.Duration
	luaTimeout     time.Duration
}

// New creates a client. Connections are lazy; readiness is the caller's job
// (call Ping and CheckPolicy during startup).
func New(opts Options) *Client {
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = DefaultCommandTimeout
	}
	if opts.LuaTimeout <= 0 {
		opts.LuaTimeout = DefaultLuaTimeout
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.PoolSize <= 0 {
		opts.PoolSize = 20
	}
	return &Client{
		rdb: goredis.NewClient(&goredis.Options{
			Addr:         opts.Addr,
			Password:     opts.Password,
			DB:           opts.DB,
			PoolSize:     opts.PoolSize,
			MinIdleConns: 2,
			DialTimeout:  opts.DialTimeout,
			ReadTimeout:  opts.CommandTimeout,
			WriteTimeout: opts.CommandTimeout,
			MaxRetries:   1,
		}),
		commandTimeout: opts.CommandTimeout,
		luaTimeout:     opts.LuaTimeout,
	}
}

// Raw returns the underlying client. Callers must not replace its timeouts.
func (c *Client) Raw() *goredis.Client { return c.rdb }

// CommandContext bounds one ordinary command (50ms by default).
func (c *Client) CommandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.commandTimeout)
}

// LuaContext bounds one EVAL/EVALSHA call (100ms by default).
func (c *Client) LuaContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.luaTimeout)
}

// Ping performs a readiness probe with a 200ms budget.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	return c.rdb.Ping(ctx).Err()
}

// CheckPolicy enforces maxmemory-policy=noeviction (T06 hard constraint 2).
// It returns an error when the policy differs or when CONFIG is unavailable:
// an unverifiable policy is treated as unsafe, because the failure mode is
// silent quota bypass.
func (c *Client) CheckPolicy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	values, err := c.rdb.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		return fmt.Errorf("redisx: read maxmemory-policy: %w", err)
	}
	policy := values["maxmemory-policy"]
	if policy != "noeviction" {
		return fmt.Errorf("%w (got %q)", ErrPolicyRejected, policy)
	}
	return nil
}

// Close releases the pool.
func (c *Client) Close() error { return c.rdb.Close() }
