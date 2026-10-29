// Command recondrill runs R-01..R-09 against a database that is not the
// package's test schema — in practice the restored `yunshu_drill` used by the
// T36 backup/restore drill.
//
// Why a CLI and not `go test ./internal/recon -run ...`: TestMain owns
// `yunshu_recon_test`, runs migrations there and every test plants and deletes
// fixtures. Pointing that suite at a restored database would (a) mutate the
// restored state, (b) make "0 差异" meaningless because the fixtures are built
// to be consistent, and (c) still leave the production entry point
// (`Daily.Run`) unexercised. This command calls the real implementation —
// `recon.Daily.Checks()` for the per-assertion breakdown and `Daily.Run()` for
// the full task (internal + upstream bill comparison + t_reconcile_diff
// writes) — with production thresholds and no overrides.
//
// It refuses to run outside ENV=local, refuses `*_test` schemas, and refuses
// the production schema (`yunshu`) unless -allow-production is set. `Run`
// writes only `t_reconcile_diff` and `t_notification`, the same two tables
// jobsd writes; it never touches balances, ledger rows or batches.
//
// Usage:
//
//	ENV=local \
//	DB_DSN='root:root@tcp(127.0.0.1:13306)/yunshu_drill?parseTime=true&loc=UTC&charset=utf8mb4' \
//	go run ./cmd/recondrill -day 2026-10-29
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/db"
	"yunshu.dev/core/internal/recon"
)

const service = "recondrill"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "recondrill:", err)
		os.Exit(1)
	}
}

func run() error {
	if env := strings.TrimSpace(os.Getenv("ENV")); !strings.EqualFold(env, "local") {
		return fmt.Errorf("drill-only (ENV=%q, want local)", env)
	}

	dayFlag := flag.String("day", time.Now().In(recon.CST).Format("2006-01-02"), "业务日 YYYY-MM-DD（UTC+8）")
	ownerFlag := flag.String("owner", "", "对账任务 owner，默认 drill-<UTC 时间戳>")
	allowProd := flag.Bool("allow-production", false, "允许 DB_DSN 指向生产库名 yunshu（真实故障恢复后使用）")
	flag.Parse()

	dsn := strings.TrimSpace(os.Getenv("DB_DSN"))
	if dsn == "" {
		return fmt.Errorf("DB_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse DB_DSN: %w", err)
	}
	schema := cfg.DBName
	if schema == "" {
		return fmt.Errorf("DB_DSN has no database name")
	}
	if strings.HasSuffix(schema, "_test") {
		return fmt.Errorf("refusing to run against test schema %q", schema)
	}
	if schema == "yunshu" && !*allowProd {
		return fmt.Errorf("refusing to run against production schema %q without -allow-production", schema)
	}

	day, err := time.ParseInLocation("2006-01-02", *dayFlag, recon.CST)
	if err != nil {
		return fmt.Errorf("parse -day: %w", err)
	}
	owner := strings.TrimSpace(*ownerFlag)
	if owner == "" {
		owner = "drill-" + time.Now().UTC().Format("20060102T150405Z")
	}

	conn, err := db.OpenMain(dsn, db.JobsPoolMaxOpen, db.MainPoolMaxIdle)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := db.Ping(ctx, conn); err != nil {
		return fmt.Errorf("ping %s: %w", schema, err)
	}

	version, tables, err := footprint(ctx, conn, schema)
	if err != nil {
		return err
	}

	logger := log.NewWithWriter(service, "drill", os.Stderr, clock.Real{})
	d := recon.NewDaily(conn, nil, logger, clock.Real{}, owner)

	fmt.Printf("recondrill schema=%s schema_version=%s tables=%d biz_date=%s owner=%s\n",
		schema, version, tables, day.Format("2006-01-02"), owner)

	// Per-assertion pass. Read-only: this is the same loop Daily.runInternal
	// uses, run separately so each R-0x gets its own count in the report.
	dayStart := day
	dayEnd := day.AddDate(0, 0, 1)
	bizDate := day.Format("2006-01-02")
	perCheck := map[string]int{}
	internalTotal := 0
	for _, c := range d.Checks() {
		diffs, err := c.Run(ctx, d, bizDate, dayStart, dayEnd)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Code, err)
		}
		perCheck[c.Code] = len(diffs)
		internalTotal += len(diffs)
		fmt.Printf("check=%s diffs=%d\n", c.Code, len(diffs))
		for i, diff := range diffs {
			if i == 5 {
				fmt.Printf("  ... %d more\n", len(diffs)-5)
				break
			}
			fmt.Printf("  subject=%s level=%s local=%d upstream=%d delta=%d\n",
				diff.Subject, diff.Level, diff.LocalValue, diff.UpstreamValue, diff.DeltaMicro)
		}
	}

	// Full task pass: internal checks + runExternal + t_reconcile_diff writes.
	// The sum must match the per-assertion pass; a mismatch means the two
	// entry points disagree and the report cannot cite either number.
	res, err := d.Run(ctx, day)
	if err != nil {
		return fmt.Errorf("daily run: %w", err)
	}
	if res.Internal != internalTotal {
		return fmt.Errorf("check_sum=%d but Daily.Run internal=%d", internalTotal, res.Internal)
	}

	fmt.Printf("run internal=%d external=%d recorded=%d changed=%d p0=%v\n",
		res.Internal, res.External, res.Recorded, res.Changed, res.P0)
	if res.P0Reason != "" {
		fmt.Printf("run p0_reason=%s\n", res.P0Reason)
	}

	if total := res.Internal + res.External; total > 0 {
		fmt.Printf("result=FAIL total_diffs=%d\n", total)
		return fmt.Errorf("%d diff(s) on %s", total, schema)
	}
	fmt.Println("result=PASS total_diffs=0")
	return nil
}

// footprint returns MAX(schema_migrations.version) and the table count, so the
// report can tie the run to the exact restored schema.
func footprint(ctx context.Context, conn *sql.DB, schema string) (string, int64, error) {
	version := "<none>"
	var v sql.NullString
	err := conn.QueryRowContext(ctx,
		"SELECT MAX(version) FROM `"+schema+"`.schema_migrations").Scan(&v)
	if err != nil && err != sql.ErrNoRows {
		// Missing schema_migrations is a legitimate state for a scratch DB;
		// any other error still fails the run.
		if !strings.Contains(err.Error(), "doesn't exist") && !strings.Contains(err.Error(), "1146") {
			return "", 0, fmt.Errorf("read schema_migrations: %w", err)
		}
	}
	if v.Valid && v.String != "" {
		version = v.String
	}

	var tables int64
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", schema,
	).Scan(&tables); err != nil {
		return "", 0, fmt.Errorf("count tables: %w", err)
	}
	return version, tables, nil
}
