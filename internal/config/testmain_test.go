package config

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/migrate"
	"yunshu.dev/core/internal/redisx/redistest"
)

// The config tests require the same real services as the ledger tests:
// MySQL on 127.0.0.1:13306 (yunshu_test) and Redis on 127.0.0.1:16379.
// Redis uses DB 15 by default so a running local development process (DB 0)
// never sees cfg:ver or cfg:snap:* written by tests.
const (
	defaultTestDSN       = "root:root@tcp(127.0.0.1:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"
	defaultTestRedisAddr = "127.0.0.1:16379"
	defaultTestRedisDB   = 15
)

var (
	testDB         *sql.DB
	testRedis      *goredis.Client
	testRedisIface Redis
	redisUnlock    func()

	originalRows       []originalConfigRow
	originalPublishMax int64
	safeDBName         = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// configSchema is the schema these tests always own, whatever TEST_DB_DSN says.
const configSchema = "yunshu_config_test"

type originalConfigRow struct {
	namespace string
	key       string
	value     string
	version   int64
	enabled   bool
	updatedBy sql.NullInt64
}

func TestMain(m *testing.M) {
	if err := setupDatabase(); err != nil {
		fmt.Fprintf(os.Stderr, "config tests: %v\n", err)
		os.Exit(1)
	}
	if err := setupRedis(); err != nil {
		fmt.Fprintf(os.Stderr, "config tests: %v\n", err)
		os.Exit(1)
	}
	if err := snapshotState(); err != nil {
		fmt.Fprintf(os.Stderr, "config tests: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	restoreConfigState()
	if testRedis != nil {
		_ = testRedis.Close()
	}
	if redisUnlock != nil {
		redisUnlock()
	}
	if testDB != nil {
		_ = testDB.Close()
	}
	os.Exit(code)
}

func setupDatabase() error {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse TEST_DB_DSN: %w", err)
	}
	// Only host and credentials are inherited from TEST_DB_DSN. The schema is
	// config-owned: these tests publish config versions, and t_config_publish /
	// cfg:ver are global counters. Sharing yunshu_test with internal/control/ops
	// (whose config handlers also publish) made TestPublishUpdatesAllStores
	// fail intermittently whenever the two packages ran concurrently.
	cfg.DBName = configSchema
	if !safeDBName.MatchString(cfg.DBName) {
		return fmt.Errorf("unsafe database name %q", cfg.DBName)
	}
	cfg.ParseTime = true
	if cfg.Loc == nil {
		cfg.Loc = time.UTC
	}
	cfg.MultiStatements = true

	dbName := cfg.DBName
	adminCfg := *cfg
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open admin connection: %w", err)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.TODO(), 2*time.Minute)
	defer cancel()

	createSQL := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs",
		dbName,
	)
	if _, err := admin.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create schema %s: %w", dbName, err)
	}

	// A previous interrupted run can leave tables but no schema_migrations.
	var tables, bookkeeping int
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?`, dbName,
	).Scan(&tables); err != nil {
		return fmt.Errorf("count tables: %w", err)
	}
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = ? AND table_name = 'schema_migrations'`, dbName,
	).Scan(&bookkeeping); err != nil {
		return fmt.Errorf("count bookkeeping: %w", err)
	}
	if tables > 0 && bookkeeping == 0 {
		if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE `%s`", dbName)); err != nil {
			return fmt.Errorf("drop incomplete schema: %w", err)
		}
		if _, err := admin.ExecContext(ctx, createSQL); err != nil {
			return fmt.Errorf("recreate schema: %w", err)
		}
	}

	testDB, err = sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open test schema: %w", err)
	}
	testDB.SetMaxOpenConns(20)
	testDB.SetMaxIdleConns(10)
	testDB.SetConnMaxLifetime(5 * time.Minute)
	if err := testDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping test schema: %w", err)
	}

	// `go test ./...` runs package binaries in parallel and every package
	// migrates this schema. GET_LOCK serialises the runners; without it two
	// runners can both apply a pending file and one fails on the duplicate
	// CREATE TABLE. The lock is connection-scoped, so keep the conn open.
	lockConn, err := testDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration lock conn: %w", err)
	}
	defer lockConn.Close()
	var locked sql.NullInt64
	if err := lockConn.QueryRowContext(ctx, `SELECT GET_LOCK('yunshu_test_migrate', 120)`).Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("migration lock failed: %v %v", err, locked)
	}
	defer func() { _, _ = lockConn.ExecContext(ctx, `SELECT RELEASE_LOCK('yunshu_test_migrate')`) }()

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	runner := migrate.NewRunner(testDB, migrations, clock.Real{}.Now)
	runner.Skip("0019", "config tests use the 0018 seed; local route rows are irrelevant")
	if _, err := runner.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func setupRedis() error {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = defaultTestRedisAddr
	}
	db := defaultTestRedisDB
	if raw := os.Getenv("TEST_REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("TEST_REDIS_DB: %w", err)
		}
		db = parsed
	}
	// DB 15 is fixed for this package. A redistest package whose hash lands on
	// 15 takes the same lock, so its FLUSHDB cannot run underneath these tests.
	unlock, err := redistest.AcquireLock(addr, db)
	if err != nil {
		return fmt.Errorf("acquire redis test lock: %w", err)
	}
	redisUnlock = unlock
	testRedis = goredis.NewClient(&goredis.Options{Addr: addr, DB: db})
	ctx, cancel := context.WithTimeout(context.TODO(), 3*time.Second)
	defer cancel()
	if err := testRedis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis %s db %d: %w", addr, db, err)
	}
	if err := testRedis.FlushDB(ctx).Err(); err != nil {
		return fmt.Errorf("flush redis test db: %w", err)
	}
	testRedisIface = NewGoRedis(testRedis)
	return nil
}

func snapshotState() error {
	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()
	rows, err := testDB.QueryContext(ctx, `
		SELECT cfg_namespace, cfg_key, cfg_value, version, enabled, updated_by
		  FROM t_config_item
		 WHERE scope = 'GLOBAL' AND scope_id = 0
		 ORDER BY cfg_key`)
	if err != nil {
		return fmt.Errorf("snapshot config items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row originalConfigRow
		if err := rows.Scan(&row.namespace, &row.key, &row.value, &row.version, &row.enabled, &row.updatedBy); err != nil {
			return fmt.Errorf("scan config item: %w", err)
		}
		originalRows = append(originalRows, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate config items: %w", err)
	}
	if err := testDB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM t_config_publish`).Scan(&originalPublishMax); err != nil {
		return fmt.Errorf("snapshot publish max id: %w", err)
	}
	return nil
}

func restoreConfigState() {
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()
	for _, row := range originalRows {
		var operator any
		if row.updatedBy.Valid {
			operator = row.updatedBy.Int64
		}
		_, _ = testDB.ExecContext(ctx, `
			UPDATE t_config_item
			   SET cfg_value = ?, version = ?, enabled = ?, updated_by = ?, updated_at = NOW(3)
			 WHERE cfg_key = ? AND scope = 'GLOBAL' AND scope_id = 0`,
			row.value, row.version, row.enabled, operator, row.key)
	}
	_, _ = testDB.ExecContext(ctx,
		`DELETE FROM t_config_publish WHERE id > ?`, originalPublishMax)
	if testRedis != nil {
		_ = testRedis.FlushDB(ctx).Err()
	}
}

// resetState restores the seed rows and Redis, and registers the same restore
// as a cleanup. Mutation tests call it first so they never inherit a
// previous test's publish.
func resetState(t *testing.T) {
	t.Helper()
	restoreConfigState()
	t.Cleanup(func() { restoreConfigState() })
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newTestPublisher() *Publisher {
	return &Publisher{DB: testDB, Redis: testRedisIface}
}

func newTestLoader(logger Logger) *Loader {
	return &Loader{Redis: testRedisIface, Store: NewStore(), Logger: logger}
}

// writeSnapshotsAt writes a complete, version-consistent set of five
// snapshots followed by cfg:ver. Snapshots are written first, exactly as the
// publisher does, so a concurrent loader never sees a new version with an old
// payload.
func writeSnapshotsAt(t *testing.T, version int64) {
	t.Helper()
	ctx := testCtx(t)
	for _, ns := range SnapshotNamespaces {
		snap := newSnapshot(ns, version, time.Now().UTC())
		payload, err := encodeSnapshot(snap)
		if err != nil {
			t.Fatalf("encode %s snapshot: %v", ns, err)
		}
		if err := testRedisIface.Set(ctx, SnapshotRedisKey(ns), string(payload), 0); err != nil {
			t.Fatalf("set %s snapshot: %v", ns, err)
		}
	}
	if err := testRedisIface.Set(ctx, RedisVersionKey, strconv.FormatInt(version, 10), 0); err != nil {
		t.Fatalf("set cfg:ver: %v", err)
	}
}

func redisString(t *testing.T, key string) string {
	t.Helper()
	ctx := testCtx(t)
	value, err := testRedisIface.Get(ctx, key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return value
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout after %s waiting for %s", timeout, what)
}

type captureLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *captureLogger) Warn(_ context.Context, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
}

func (l *captureLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.msgs))
	copy(out, l.msgs)
	return out
}

func (l *captureLogger) contains(sub string) bool {
	for _, msg := range l.messages() {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}
