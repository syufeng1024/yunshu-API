package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/migrate"
)

// testDB is the real MySQL schema used by every ledger test. It is created on
// first run, migrated with the production migration set, and deliberately NOT
// dropped afterwards so the operator can inspect the rows.
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
		fmt.Fprintf(os.Stderr, "ledger tests: parse TEST_DB_DSN: %v\n", err)
		os.Exit(1)
	}
	if cfg.DBName == "" {
		cfg.DBName = "yunshu_test"
	}
	if !safeDBName.MatchString(cfg.DBName) {
		fmt.Fprintf(os.Stderr, "ledger tests: unsafe database name %q\n", cfg.DBName)
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
		fmt.Fprintf(os.Stderr, "ledger tests: open admin: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "ledger tests: create schema %s: %v\n", dbName, err)
		os.Exit(1)
	}

	// A previous interrupted run can leave tables but no schema_migrations.
	// In that case the migration files would try to CREATE existing tables.
	var tables, bookkeeping int
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?`, dbName,
	).Scan(&tables); err != nil {
		fmt.Fprintf(os.Stderr, "ledger tests: count tables: %v\n", err)
		os.Exit(1)
	}
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = ? AND table_name = 'schema_migrations'`, dbName,
	).Scan(&bookkeeping); err != nil {
		fmt.Fprintf(os.Stderr, "ledger tests: count bookkeeping: %v\n", err)
		os.Exit(1)
	}
	if tables > 0 && bookkeeping == 0 {
		if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE `%s`", dbName)); err != nil {
			fmt.Fprintf(os.Stderr, "ledger tests: drop incomplete schema: %v\n", err)
			os.Exit(1)
		}
		if _, err := admin.ExecContext(ctx, createSQL); err != nil {
			fmt.Fprintf(os.Stderr, "ledger tests: recreate schema: %v\n", err)
			os.Exit(1)
		}
	}

	testDB, err = sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledger tests: open test schema: %v\n", err)
		os.Exit(1)
	}
	testDB.SetMaxOpenConns(20)
	testDB.SetMaxIdleConns(10)
	testDB.SetConnMaxLifetime(5 * time.Minute)

	if err := testDB.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ledger tests: ping test schema: %v\n", err)
		os.Exit(1)
	}

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledger tests: load migrations: %v\n", err)
		os.Exit(1)
	}
	runner := migrate.NewRunner(testDB, migrations, clock.Real{}.Now)
	runner.Skip("0019", "ledger tests use their own fixtures; local seed rows are irrelevant")
	if _, err := runner.Up(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ledger tests: apply migrations: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	testDB.Close()
	os.Exit(code)
}
