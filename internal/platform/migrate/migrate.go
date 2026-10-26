package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// createSchemaMigrationsSQL is the bookkeeping table itself. It is created
// before any migration runs and is never versioned.
const createSchemaMigrationsSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version char(4) NOT NULL,
  name varchar(128) NOT NULL,
  checksum char(64) NOT NULL,
  applied_at datetime(3) NOT NULL,
  execution_ms int NOT NULL,
  success tinyint(1) NOT NULL,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs`

// Applied is one row of schema_migrations.
type Applied struct {
	Version     string
	Name        string
	Checksum    string
	AppliedAt   time.Time
	ExecutionMS int
	Success     bool
}

// Result describes one executed up/down file.
type Result struct {
	Version   string
	Name      string
	Direction string
	Duration  time.Duration
}

// Status is one row for the status command.
type Status struct {
	Version       string
	Name          string
	Applied       bool
	Success       bool
	ChecksumMatch bool
	AppliedAt     time.Time
}

// Verify compares every applied row with the current file bytes. A mismatch is
// fatal and must be resolved by rolling the code back, never by rewriting the
// applied file: the whole point of the checksum is that history is immutable.
func Verify(applied []Applied, migrations []Migration) error {
	byVersion := make(map[string]Migration, len(migrations))
	for _, m := range migrations {
		byVersion[m.Version] = m
	}
	for _, a := range applied {
		m, ok := byVersion[a.Version]
		if !ok {
			return fmt.Errorf("迁移 %s 已记录在 schema_migrations，但对应 .sql 文件不存在", a.Version)
		}
		if a.Checksum != m.Checksum {
			return fmt.Errorf(
				"迁移 %s（%s）的 checksum 不匹配：库中 %s，文件 %s；已应用迁移不得修改，请回滚代码而不是改脚本",
				a.Version, m.UpFile, a.Checksum, m.Checksum,
			)
		}
	}
	return nil
}

// Runner executes migrations against one database.
type Runner struct {
	db         *sql.DB
	migrations []Migration
	now        func() time.Time
	// skip maps a version to the reason it must not run in this process
	// environment. A skipped migration is not recorded in schema_migrations:
	// it stays pending, so a later run with the right ENV can still apply it.
	skip map[string]string
}

// NewRunner builds a runner. now must return UTC; main passes clock.Real{}.Now.
func NewRunner(db *sql.DB, migrations []Migration, now func() time.Time) *Runner {
	return &Runner{db: db, migrations: migrations, now: now}
}

// Skip marks a version as not applicable in the current environment. Up logs a
// WARN and continues instead of failing: a development-only seed must never
// block a production upgrade.
func (r *Runner) Skip(version, reason string) {
	if r.skip == nil {
		r.skip = make(map[string]string)
	}
	r.skip[version] = reason
}

// EnsureTable creates schema_migrations if it is missing.
func (r *Runner) EnsureTable(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, createSchemaMigrationsSQL); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}
	return nil
}

// Applied reads the bookkeeping table in version order.
func (r *Runner) Applied(ctx context.Context) ([]Applied, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT version, name, checksum, applied_at, execution_ms, success
		   FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("读取 schema_migrations 失败: %w", err)
	}
	defer rows.Close()

	var applied []Applied
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum, &a.AppliedAt, &a.ExecutionMS, &a.Success); err != nil {
			return nil, fmt.Errorf("解析 schema_migrations 失败: %w", err)
		}
		applied = append(applied, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 schema_migrations 失败: %w", err)
	}
	return applied, nil
}

// Up applies every pending migration in version order. Versions need not be
// contiguous: the loader sorts by version, and 0007 / 0008 are permanent gaps.
func (r *Runner) Up(ctx context.Context) ([]Result, error) {
	if err := r.nowGuard(); err != nil {
		return nil, err
	}
	if err := r.EnsureTable(ctx); err != nil {
		return nil, err
	}
	applied, err := r.Applied(ctx)
	if err != nil {
		return nil, err
	}
	if err := Verify(applied, r.migrations); err != nil {
		return nil, err
	}

	done := make(map[string]Applied, len(applied))
	for _, a := range applied {
		if !a.Success {
			return nil, fmt.Errorf(
				"迁移 %s 上次执行失败（success=0）；确认现场后执行 DELETE FROM schema_migrations WHERE version='%s' 再重试",
				a.Version, a.Version,
			)
		}
		done[a.Version] = a
	}

	var results []Result
	for _, m := range r.migrations {
		if _, ok := done[m.Version]; ok {
			continue
		}
		if reason, ok := r.skip[m.Version]; ok {
			fmt.Fprintf(os.Stderr, "WARN: 迁移 %s（%s）已跳过：%s\n", m.Version, m.Name, reason)
			results = append(results, Result{Version: m.Version, Name: m.Name, Direction: "skip"})
			continue
		}
		result, err := r.applyUp(ctx, m)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func (r *Runner) applyUp(ctx context.Context, m Migration) (Result, error) {
	started := r.now()
	_, execErr := r.db.ExecContext(ctx, m.UpSQL)
	elapsed := r.now().Sub(started)
	result := Result{Version: m.Version, Name: m.Name, Direction: "up", Duration: elapsed}

	if execErr != nil {
		// Record the failure so the next start refuses to silently replay a
		// half-applied DDL file. MySQL DDL auto-commits, so "half" is possible.
		_ = r.record(ctx, m, elapsed, false)
		return result, fmt.Errorf("执行迁移 %s 失败: %w", m.UpFile, execErr)
	}
	if err := r.record(ctx, m, elapsed, true); err != nil {
		return result, fmt.Errorf("迁移 %s 已执行但写入 schema_migrations 失败: %w", m.UpFile, err)
	}
	return result, nil
}

// Down rolls back the newest steps migrations. steps <= 0 means everything.
func (r *Runner) Down(ctx context.Context, steps int) ([]Result, error) {
	if err := r.nowGuard(); err != nil {
		return nil, err
	}
	if err := r.EnsureTable(ctx); err != nil {
		return nil, err
	}
	applied, err := r.Applied(ctx)
	if err != nil {
		return nil, err
	}
	if err := Verify(applied, r.migrations); err != nil {
		return nil, err
	}
	if len(applied) == 0 {
		return nil, fmt.Errorf("没有已应用的迁移，无需回滚")
	}

	for _, a := range applied {
		if !a.Success {
			return nil, fmt.Errorf("迁移 %s 上次执行失败（success=0），请先人工处理", a.Version)
		}
	}

	if steps <= 0 {
		steps = len(applied)
	}
	if steps > len(applied) {
		return nil, fmt.Errorf("请求回滚 %d 条，但只有 %d 条已应用", steps, len(applied))
	}
	byVersion := make(map[string]Migration, len(r.migrations))
	for _, m := range r.migrations {
		byVersion[m.Version] = m
	}

	targets := applied[len(applied)-steps:]
	sort.Slice(targets, func(i, j int) bool { return targets[i].Version > targets[j].Version })

	var results []Result
	for _, a := range targets {
		m, ok := byVersion[a.Version]
		if !ok {
			return results, fmt.Errorf("迁移 %s 的记录存在但文件缺失，拒绝回滚", a.Version)
		}
		result, err := r.applyDown(ctx, m)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func (r *Runner) applyDown(ctx context.Context, m Migration) (Result, error) {
	started := r.now()
	_, execErr := r.db.ExecContext(ctx, m.DownSQL)
	elapsed := r.now().Sub(started)
	result := Result{Version: m.Version, Name: m.Name, Direction: "down", Duration: elapsed}
	if execErr != nil {
		return result, fmt.Errorf("执行回滚 %s 失败: %w", m.DownFile, execErr)
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version = ?`, m.Version); err != nil {
		return result, fmt.Errorf("回滚 %s 已执行但删除 schema_migrations 记录失败: %w", m.DownFile, err)
	}
	return result, nil
}

func (r *Runner) record(ctx context.Context, m Migration, elapsed time.Duration, success bool) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at, execution_ms, success)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   name = VALUES(name), checksum = VALUES(checksum), applied_at = VALUES(applied_at),
		   execution_ms = VALUES(execution_ms), success = VALUES(success)`,
		m.Version, m.Name, m.Checksum, r.now().UTC(), elapsed.Milliseconds(), success,
	)
	return err
}

// Status reports every file and whether it is applied.
func (r *Runner) Status(ctx context.Context) ([]Status, error) {
	if err := r.EnsureTable(ctx); err != nil {
		return nil, err
	}
	applied, err := r.Applied(ctx)
	if err != nil {
		return nil, err
	}
	byVersion := make(map[string]Applied, len(applied))
	for _, a := range applied {
		byVersion[a.Version] = a
	}

	statuses := make([]Status, 0, len(r.migrations))
	for _, m := range r.migrations {
		s := Status{Version: m.Version, Name: m.Name}
		if a, ok := byVersion[m.Version]; ok {
			s.Applied = true
			s.Success = a.Success
			s.ChecksumMatch = a.Checksum == m.Checksum
			s.AppliedAt = a.AppliedAt
		}
		statuses = append(statuses, s)
	}
	return statuses, nil
}

func (r *Runner) nowGuard() error {
	if r.db == nil {
		return fmt.Errorf("migrate: 数据库连接为空")
	}
	if r.now == nil {
		return fmt.Errorf("migrate: 未注入时钟")
	}
	return nil
}

// NormalizeDSN adds the driver options the runner depends on. The DSN in .env
// already carries parseTime and loc; multiStatements is required because one
// migration file may contain several statements (0006 has three DDL blocks).
func NormalizeDSN(dsn string) string {
	additions := make([]string, 0, 3)
	if !strings.Contains(dsn, "multiStatements=") {
		additions = append(additions, "multiStatements=true")
	}
	if !strings.Contains(dsn, "parseTime=") {
		additions = append(additions, "parseTime=true")
	}
	if !strings.Contains(dsn, "loc=") {
		additions = append(additions, "loc=UTC")
	}
	if len(additions) == 0 {
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + strings.Join(additions, "&")
}
