// Package redistest provides the real-Redis fixture shared by the packages
// that need Redis. It intentionally does not use miniredis: the Lua scripts
// under test rely on real EVAL atomicity and real server-side LIST/STREAM
// commands.
//
// Each package gets its own logical database, derived from the directory of
// the caller that invokes Setup (normally a TestMain). `go test ./...` runs
// package binaries concurrently; before T21 every package shared DB 14 and a
// FLUSHDB from one binary could land inside another binary's test (T09 measured
// roughly one failure in three full runs). Hashing the package directory to
// DB 1..15 means two packages only collide if the hash collides, and the
// filesystem lock still serialises that case.
//
// TEST_REDIS_DB overrides the hash. TEST_REDIS_ADDR selects the server
// (default 127.0.0.1:16379). DB 0 (local development) is never touched.
//
// internal/config keeps DB 15 for its own TestMain; it does not call Setup but
// it does take the same lock through AcquireLock, so a package whose hash lands
// on 15 cannot flush underneath it.
package redistest

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// DefaultAddr matches the local compose overlay in scripts/dev.sh.
	DefaultAddr = "127.0.0.1:16379"
	// DefaultDB is used only when the caller's package cannot be identified.
	// Setup normally derives the database from the caller's directory.
	DefaultDB = 14
	// DBInUse is the number of logical databases the hash spreads over.
	DBInUse = 15

	lockTimeout = 90 * time.Second
	lockStale   = 30 * time.Second
)

// Env is one opened test database.
type Env struct {
	Addr   string
	DB     int
	Client *goredis.Client
}

// PackageDB maps a package directory to DB 1..DBInUse. The mapping is stable
// across processes, which is what lets two test binaries agree on which
// database belongs to them without coordination.
func PackageDB(pkgPath string) int {
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(pkgPath))))
	if clean == "" {
		return DefaultDB
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(clean))
	return int(h.Sum32()%DBInUse) + 1
}

// callerPackage returns the directory of the first frame outside this package.
// TestMain calls Setup directly, so frame 1 is usually the answer; the loop
// keeps the mapping correct if a package wraps Setup in a helper.
func callerPackage() string {
	for skip := 1; skip < 8; skip++ {
		_, file, _, ok := runtime.Caller(skip)
		if !ok {
			break
		}
		if strings.Contains(filepath.ToSlash(file), "/internal/redisx/redistest/") {
			continue
		}
		return filepath.Dir(file)
	}
	return ""
}

// Open reads the environment and pings Redis. It does not flush; Setup does.
func Open() (*Env, error) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = DefaultAddr
	}
	db := PackageDB(callerPackage())
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
	if db < 1 || db > DBInUse {
		return nil, fmt.Errorf("redistest: database %d outside 1..%d", db, DBInUse)
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

	unlock, err := AcquireLock(env.Addr, env.DB)
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

// AcquireLock serialises binaries that share one logical database. Setup uses
// it directly; a package with its own TestMain (internal/config) calls it so
// a hash collision cannot flush under a running suite.
func AcquireLock(addr string, db int) (func(), error) {
	env := &Env{Addr: addr, DB: db}
	return acquireLock(env)
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
