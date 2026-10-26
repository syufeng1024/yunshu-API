package settle

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

// testDB is the real MySQL schema (yunshu_test). Settlement is a database
// transaction; a fake would not prove the acceptance rows.
var testDB *sql.DB

const defaultTestDSN = "root:root@tcp(127.0.0.1:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"

var safeDBName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: parse TEST_DB_DSN: %v\n", err)
		os.Exit(1)
	}
	if cfg.DBName == "" {
		cfg.DBName = "yunshu_test"
	}
	if !safeDBName.MatchString(cfg.DBName) {
		fmt.Fprintf(os.Stderr, "settle tests: unsafe database name %q\n", cfg.DBName)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "settle tests: open admin: %v\n", err)
		os.Exit(1)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	createSQL := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs",
		dbName,
	)
	if _, err := admin.ExecContext(ctx, createSQL); err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: create schema: %v\n", err)
		os.Exit(1)
	}

	// Recover from an interrupted run that left tables without bookkeeping.
	var tables, bookkeeping int
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?`, dbName,
	).Scan(&tables); err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: count tables: %v\n", err)
		os.Exit(1)
	}
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = ? AND table_name = 'schema_migrations'`, dbName,
	).Scan(&bookkeeping); err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: count bookkeeping: %v\n", err)
		os.Exit(1)
	}
	if tables > 0 && bookkeeping == 0 {
		if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE `%s`", dbName)); err != nil {
			fmt.Fprintf(os.Stderr, "settle tests: drop incomplete schema: %v\n", err)
			os.Exit(1)
		}
		if _, err := admin.ExecContext(ctx, createSQL); err != nil {
			fmt.Fprintf(os.Stderr, "settle tests: recreate schema: %v\n", err)
			os.Exit(1)
		}
	}

	testDB, err = sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: open schema: %v\n", err)
		os.Exit(1)
	}
	testDB.SetMaxOpenConns(20)
	testDB.SetMaxIdleConns(10)
	testDB.SetConnMaxLifetime(5 * time.Minute)
	if err := testDB.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: ping schema: %v\n", err)
		os.Exit(1)
	}

	// Several packages migrate the same schema in parallel. The advisory lock
	// serialises this package against jobs, which also needs migration 0021.
	conn, err := testDB.Conn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: lock conn: %v\n", err)
		os.Exit(1)
	}
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('yunshu_test_migrate', 120)`).Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		fmt.Fprintf(os.Stderr, "settle tests: migration lock failed: %v %v\n", err, locked)
		os.Exit(1)
	}

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: load migrations: %v\n", err)
		os.Exit(1)
	}
	runner := migrate.NewRunner(testDB, migrations, clock.Real{}.Now)
	runner.Skip("0019", "settlement tests use their own routes; local seed rows are irrelevant")
	if _, err := runner.Up(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "settle tests: apply migrations: %v\n", err)
		os.Exit(1)
	}
	_, _ = conn.ExecContext(ctx, `SELECT RELEASE_LOCK('yunshu_test_migrate')`)
	_ = conn.Close()

	code := redistest.Setup(m)
	_ = testDB.Close()
	os.Exit(code)
}

// newRequestID returns a 36-character hex id, the width of char(36) columns.
func newRequestID(t *testing.T) string {
	t.Helper()
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}
