// Package redistest provides the real-Redis fixture shared by the T06
// packages. It intentionally does not use miniredis: the Lua scripts under
// test rely on real EVAL atomicity and real server-side LIST/STREAM commands.
//
// All T06 packages use TEST_REDIS_ADDR / TEST_REDIS_DB (default
// 127.0.0.1:16379 / DB 15, the T05 convention) and FLUSHDB before and after a
// test binary. Because `go test ./...` runs package binaries concurrently, a
// filesystem lock serialises binaries that share the same Redis database.
// DB 0 (local development) is never touched.
package redistest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// DefaultAddr matches the local compose overlay in scripts/dev.sh.
	DefaultAddr = "127.0.0.1:16379"
	// DefaultDB is DB 15, matching the T05 config tests.
	DefaultDB = 15

	lockTimeout = 90 * time.Second
	lockStale   = 30 * time.Second
)

// Env is one opened test database.
type Env struct {
	Addr   string
	DB     int
	Client *goredis.Client
}

// Open reads the environment and pings Redis. It does not flush; Setup does.
func Open() (*Env, error) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = DefaultAddr
	}
	db := DefaultDB
	if raw := os.Getenv("TEST_REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("TEST_REDIS_DB: %w", err)
		}
		db = parsed
	}
	if db == 0 {
		// Never let a test binary wipe the local development keyspace.
		return nil, errors.New("redistest: refusing TEST_REDIS_DB=0")
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr, DB: db})
	ctx, cancel := context.WithTimeout(context.TODO(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redistest: ping %s db %d: %w", addr, db, err)
	}
	return &Env{Addr: addr, DB: db, Client: client}, nil
}

// Flush empties the test database.
func (e *Env) Flush(ctx context.Context) error { return e.Client.FlushDB(ctx).Err() }

// Close releases the pool.
func (e *Env) Close() error { return e.Client.Close() }

var current *Env

// Setup is the TestMain body. It takes the cross-binary lock, flushes, runs
// m, flushes again and releases the lock. The caller only needs to os.Exit
// with the returned code.
func Setup(m Runner) int {
	env, err := Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "redistest: %v\n", err)
		return 1
	}
	defer env.Close()
	current = env
	defer func() { current = nil }()

	unlock, err := acquireLock(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "redistest: %v\n", err)
		return 1
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	if err := env.Flush(ctx); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "redistest: flush: %v\n", err)
		return 1
	}
	cancel()

	code := m.Run()

	ctx, cancel = context.WithTimeout(context.TODO(), 10*time.Second)
	_ = env.Flush(ctx)
	cancel()
	return code
}

// Addr returns the address of the database opened by Setup.
func Addr() string { return mustCurrent().Addr }

// DB returns the logical database opened by Setup.
func DB() int { return mustCurrent().DB }

// Client returns the pool opened by Setup. It is valid only while m.Run is
// executing.
func Client() *goredis.Client { return mustCurrent().Client }

func mustCurrent() *Env {
	if current == nil {
		panic("redistest: Setup has not run")
	}
	return current
}

// Runner is the subset of testing.M used by Setup, kept as an interface so
// this package does not import testing from non-test code.
type Runner interface {
	Run() int
}

func lockPath(env *Env) string {
	name := fmt.Sprintf("yunshu-redis-test-%s-%d.lock", sanitize(env.Addr), env.DB)
	return filepath.Join(os.TempDir(), name)
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func acquireLock(env *Env) (func(), error) {
	path := lockPath(env)
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d %d", os.Getpid(), time.Now().Unix())
			_ = f.Close()
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case now := <-ticker.C:
						_ = os.Chtimes(path, now, now)
					}
				}
			}()
			return func() {
				close(stop)
				<-done
				_ = os.Remove(path)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("redistest: create lock %s: %w", path, err)
		}
		// A killed test binary leaves the file behind. Age is the only
		// portable liveness signal without platform-specific file locks.
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("redistest: timed out waiting for %s; remove it if no test is running", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
