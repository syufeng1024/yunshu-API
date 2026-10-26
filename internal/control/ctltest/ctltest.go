// Package ctltest is the shared fixture for the control-plane package tests.
//
// It is a normal package, not a _test.go file, because two test binaries
// (internal/control/key and internal/control/model) need the same schema setup.
// Only *_test.go files import it, so it never enters a production binary.
//
// The tests run against the real yunshu_test schema and the real local Redis,
// following the ledger/settle/config convention: a fake database would not
// prove the SQL, and the console queries are all SQL.
package ctltest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/migrate"
)

// DefaultDSN matches the local compose overlay in scripts/dev.sh.
const DefaultDSN = "root:root@tcp(127.0.0.1:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"

var safeDBName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// SetupDB creates (if needed) and migrates the test schema. The caller owns
// the returned pool and must close it.
//
// Migration is serialised with a MySQL advisory lock because `go test ./...`
// runs package binaries concurrently and several already migrate this schema.
func SetupDB() (*sql.DB, error) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		dsn = DefaultDSN
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse TEST_DB_DSN: %w", err)
	}
	if cfg.DBName == "" {
		cfg.DBName = "yunshu_test"
	}
	if !safeDBName.MatchString(cfg.DBName) {
		return nil, fmt.Errorf("unsafe database name %q", cfg.DBName)
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
		return nil, fmt.Errorf("open admin connection: %w", err)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.TODO(), 2*time.Minute)
	defer cancel()

	createSQL := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs",
		dbName,
	)
	if _, err := admin.ExecContext(ctx, createSQL); err != nil {
		return nil, fmt.Errorf("create schema %s: %w", dbName, err)
	}

	// Recover from an interrupted run that left tables without bookkeeping.
	var tables, bookkeeping int
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?`, dbName,
	).Scan(&tables); err != nil {
		return nil, fmt.Errorf("count tables: %w", err)
	}
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = ? AND table_name = 'schema_migrations'`, dbName,
	).Scan(&bookkeeping); err != nil {
		return nil, fmt.Errorf("count bookkeeping: %w", err)
	}
	if tables > 0 && bookkeeping == 0 {
		if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE `%s`", dbName)); err != nil {
			return nil, fmt.Errorf("drop incomplete schema: %w", err)
		}
		if _, err := admin.ExecContext(ctx, createSQL); err != nil {
			return nil, fmt.Errorf("recreate schema: %w", err)
		}
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open schema: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping schema: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lock conn: %w", err)
	}
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('yunshu_test_migrate', 120)`).Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("migration lock failed: %v %v", err, locked)
	}
	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	runner := migrate.NewRunner(db, migrations, clock.Real{}.Now)
	runner.Skip("0019", "control tests create their own tables rows; the local seed is irrelevant")
	if _, err := runner.Up(ctx); err != nil {
		_, _ = conn.ExecContext(ctx, `SELECT RELEASE_LOCK('yunshu_test_migrate')`)
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	_, _ = conn.ExecContext(ctx, `SELECT RELEASE_LOCK('yunshu_test_migrate')`)
	_ = conn.Close()
	return db, nil
}
