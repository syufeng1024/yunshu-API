package jobs

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

const defaultTestDSN = "root:root@tcp(127.0.0.1:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"

var (
	testDB     *sql.DB
	safeDBName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func TestMain(m *testing.M) {
	if err := setupDatabase(); err != nil {
		fmt.Fprintf(os.Stderr, "jobs tests: %v\n", err)
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
	if cfg.DBName == "" {
		cfg.DBName = "yunshu_test"
	}
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

	// settle and jobs both migrate the same schema. The advisory lock makes
	// 0021 apply once even when the two packages start concurrently.
	conn, err := testDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("lock conn: %w", err)
	}
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('yunshu_test_migrate', 120)`).Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		return fmt.Errorf("migration lock: %v %v", err, locked)
	}

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	runner := migrate.NewRunner(testDB, migrations, clock.Real{}.Now)
	runner.Skip("0019", "jobs tests do not read routing snapshots")
	if _, err := runner.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	_, _ = conn.ExecContext(ctx, `SELECT RELEASE_LOCK('yunshu_test_migrate')`)
	_ = conn.Close()
	return nil
}

func randomJob(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "job_" + hex.EncodeToString(b[:])
}
