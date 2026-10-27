package migrate

// T21 卡面 A10：已应用迁移的 checksum 被篡改时，Up 必须拒绝启动。
//
// TestChecksumChangeDetected 只证明 Verify 这个纯函数会报错；它发现不了
// “Up 是否真的调用了 Verify、报错之后是否还会继续应用后面的迁移”。这里走
// 真实 MySQL：建临时 schema → 应用两条迁移 → 只给其中一个 up 文件追加一行
// 注释 → 再 Up。断言三件事：
//   1. 第二次 Up 返回错误，文案含「checksum 不匹配」与被篡改的文件名；
//   2. 第二条 Up 的 results 为空，没有一条新迁移被应用；
//   3. 为了让第 2 点可证伪，篡改后的文件集里额外放了一条 pending 的 0003，
//      断言它的表在库里不存在——若引擎先跑后校验，这条表就会被建出来。
//
// schema 独立于 yunshu_test，互不依赖；建库失败直接 Fatalf，不 skip。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/platform/clock"
)

// 与 scripts/test-env.sh 的候选顺序一致：本机原生 MySQL 在 3306，compose
// 覆盖层在 13306。TEST_DB_DSN 一旦设置就只认它，不再回退。
const (
	localDSN3306  = "root:123456@tcp(127.0.0.1:3306)/?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"
	localDSN13306 = "root:root@tcp(127.0.0.1:13306)/?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"
)

// serverDSN 返回一个不带 DBName 的配置：用例自己建临时 schema，不碰
// yunshu_test。连不上任何候选地址时直接失败——CI 的 MySQL service 必须在线。
func serverDSN(t *testing.T) *mysql.Config {
	t.Helper()

	candidates := []string{}
	if dsn := os.Getenv("TEST_DB_DSN"); dsn != "" {
		candidates = append(candidates, dsn)
	} else {
		candidates = append(candidates, localDSN3306, localDSN13306)
	}

	var lastErr error
	for _, raw := range candidates {
		cfg, err := mysql.ParseDSN(raw)
		if err != nil {
			lastErr = fmt.Errorf("解析 DSN %q 失败: %w", raw, err)
			continue
		}
		cfg.DBName = ""
		cfg.ParseTime = true
		if cfg.Loc == nil {
			cfg.Loc = time.UTC
		}
		cfg.MultiStatements = true

		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			lastErr = fmt.Errorf("打开 %s 失败: %w", cfg.Addr, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()
		_ = db.Close()
		if err == nil {
			return cfg
		}
		lastErr = fmt.Errorf("连接 %s 失败: %w", cfg.Addr, err)
	}
	t.Fatalf("MySQL 不可用（TEST_DB_DSN 未设置时依次尝试 3306 root/123456 与 13306 root/root）：%v", lastErr)
	return nil
}

func tempSchemaName(t *testing.T) string {
	t.Helper()
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("生成临时 schema 名失败: %v", err)
	}
	return "yunshu_mig_ck_" + hex.EncodeToString(suffix[:])
}

// baseMigrationsFS 是两条合法的 up/down 迁移，DDL 只建一张探针表。
func baseMigrationsFS() fstest.MapFS {
	const engine = " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;\n"
	return fstest.MapFS{
		"sql/mysql/0001_create_probe.sql":       {Data: []byte("CREATE TABLE t_probe (id int)" + engine)},
		"sql/mysql/0001_create_probe.down.sql":  {Data: []byte("DROP TABLE t_probe;\n")},
		"sql/mysql/0002_create_probe2.sql":      {Data: []byte("CREATE TABLE t_probe2 (id int)" + engine)},
		"sql/mysql/0002_create_probe2.down.sql": {Data: []byte("DROP TABLE t_probe2;\n")},
	}
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("查询 %q 失败: %v", query, err)
	}
	return n
}

func TestAppliedMigrationChecksumTamperIsRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := serverDSN(t)
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("打开管理连接失败: %v", err)
	}
	// t.Cleanup 在函数返回之后才执行，defer 会先跑；管理连接必须在 DROP
	// DATABASE 之后才关闭，所以这里用 Cleanup 收尾而不是 defer。
	t.Cleanup(func() { _ = admin.Close() })

	schema := tempSchemaName(t)
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs", schema,
	)); err != nil {
		t.Fatalf("创建临时 schema %s 失败（CI 的 MySQL service 以 root 运行，应当有建库权限，不跳过）: %v", schema, err)
	}

	testCfg := *cfg
	testCfg.DBName = schema
	db, err := sql.Open("mysql", testCfg.FormatDSN())
	if err != nil {
		t.Fatalf("打开临时 schema 失败: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("连接临时 schema %s 失败: %v", schema, err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := admin.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", schema)); err != nil {
			t.Errorf("清理临时 schema %s 失败: %v", schema, err)
		}
	})

	migrations, err := Load(baseMigrationsFS(), MySQLDir)
	if err != nil {
		t.Fatalf("加载初始迁移集失败: %v", err)
	}
	runner := NewRunner(db, migrations, clock.Real{}.Now)

	results, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("首次 Up 失败: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("首次 Up 应用了 %d 条迁移，期望 2 条", len(results))
	}
	if n := countRows(t, ctx, db, `SELECT COUNT(*) FROM schema_migrations`); n != 2 {
		t.Fatalf("schema_migrations 行数 = %d，期望 2", n)
	}
	before, err := runner.Applied(ctx)
	if err != nil {
		t.Fatalf("读取 schema_migrations 失败: %v", err)
	}

	// 只改 0001 的注释；0002 保持原样作为对照。0003 是新增的 pending 迁移，
	// 用来证明 Up 在校验失败时不会先跑任何待应用迁移。
	tampered := baseMigrationsFS()
	original := tampered["sql/mysql/0001_create_probe.sql"].Data
	tampered["sql/mysql/0001_create_probe.sql"] = &fstest.MapFile{
		Data: append(append([]byte(nil), original...), []byte("\n-- 卡面 A10：只追加一行注释\n")...),
	}
	tampered["sql/mysql/0003_create_probe3.sql"] = &fstest.MapFile{
		Data: []byte("CREATE TABLE t_probe3 (id int) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;\n"),
	}
	tampered["sql/mysql/0003_create_probe3.down.sql"] = &fstest.MapFile{
		Data: []byte("DROP TABLE t_probe3;\n"),
	}

	tamperedMigrations, err := Load(tampered, MySQLDir)
	if err != nil {
		t.Fatalf("加载篡改后的迁移集失败: %v", err)
	}
	if tamperedMigrations[0].Checksum == migrations[0].Checksum {
		t.Fatal("只改注释后 0001 的 checksum 未变化，用例前提不成立")
	}
	if tamperedMigrations[1].Checksum != migrations[1].Checksum {
		t.Fatal("未篡改的 0002 checksum 不应变化")
	}

	runner = NewRunner(db, tamperedMigrations, clock.Real{}.Now)
	results, err = runner.Up(ctx)
	if err == nil {
		t.Fatal("已应用迁移被篡改后，Up 必须报错")
	}
	for _, want := range []string{"checksum 不匹配", "0001_create_probe.sql"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误文案未包含 %q: %v", want, err)
		}
	}
	t.Logf("篡改后 Up 拒绝执行：%v", err)
	if len(results) != 0 {
		t.Fatalf("篡改后 Up 应用了 %d 条迁移，期望 0：%+v", len(results), results)
	}

	// 第二次 Up 没有应用任何新迁移：0003 的表不存在，记录仍是两条且 checksum 未变。
	if n := countRows(t, ctx, db,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 't_probe3'`,
		schema,
	); n != 0 {
		t.Fatalf("校验失败后仍创建了 t_probe3，说明 Up 先执行了待应用迁移")
	}
	if n := countRows(t, ctx, db, `SELECT COUNT(*) FROM schema_migrations`); n != 2 {
		t.Fatalf("篡改后 schema_migrations 行数 = %d，期望仍为 2", n)
	}
	after, err := runner.Applied(ctx)
	if err != nil {
		t.Fatalf("篡改后读取 schema_migrations 失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("篡改后已应用条数 = %d，期望 %d", len(after), len(before))
	}
	for i := range before {
		if after[i].Version != before[i].Version || after[i].Checksum != before[i].Checksum {
			t.Fatalf("篡改后 %s 的记录被改写：%+v → %+v", before[i].Version, before[i], after[i])
		}
	}
}
