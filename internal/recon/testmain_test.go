package recon

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/migrate"
	"yunshu.dev/core/internal/redisx/redistest"
)

// The recon tests own a dedicated schema. They scan global tables (R-01..R-08
// are not date-scoped), so sharing yunshu_test with the settle/jobs binaries —
// which run concurrently under `go test -p 2` — would make every scan depend on
// another package's fixtures.
const defaultTestDSN = "root:root@tcp(127.0.0.1:13306)/yunshu_recon_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"

// reconSchema is the schema these tests always own, whatever TEST_DB_DSN says.
const reconSchema = "yunshu_recon_test"

var (
	testDB     *sql.DB
	safeDBName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func TestMain(m *testing.M) {
	if err := setupDatabase(); err != nil {
		fmt.Fprintf(os.Stderr, "recon tests: %v\n", err)
		os.Exit(1)
	}
	code := redistest.Setup(m)
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
	// Only host and credentials are inherited from TEST_DB_DSN. The schema name
	// stays recon-owned: CI exports a single TEST_DB_DSN pointing at
	// yunshu_test for every package, and if recon joined it the global scans
	// above would (a) read another package's fixtures and (b) race that
	// package's migration runner under `go test -p N`.
	cfg.DBName = reconSchema
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
		return fmt.Errorf("open admin: %w", err)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := admin.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs", dbName,
	)); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	testDB, err = sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open schema: %w", err)
	}
	testDB.SetMaxOpenConns(10)
	testDB.SetMaxIdleConns(5)
	testDB.SetConnMaxLifetime(5 * time.Minute)
	if err := testDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping schema: %w", err)
	}

	// One advisory lock per schema: two `go test` invocations against the same
	// database must not race the migration table.
	conn, err := testDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("lock conn: %w", err)
	}
	var locked sql.NullInt64
	// Lock name follows the schema actually in use, so a package that ends up
	// on the same schema through TEST_DB_DSN still serialises against it.
	lockName := dbName + "_migrate"
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 120)`, lockName).Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("migration lock: %v %v", err, locked)
	}

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	runner := migrate.NewRunner(testDB, migrations, clock.Real{}.Now)
	runner.Skip("0019", "recon tests create their own model/channel fixtures")
	if _, err := runner.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	_, _ = conn.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, lockName)
	_ = conn.Close()
	return nil
}

// rid builds a 36-character request_id (t_usage_record.request_id is char(36)).
func rid(tag string, i int) string {
	return fmt.Sprintf("%s-%08d-0000-0000-000000000000", tag, i)
}

// entryNo builds a 26-character entry_no (char(26)).
func entryNo(tag string, i int) string {
	return fmt.Sprintf("%-4s%022d", tag, i)
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}
