//go:build ignore

// gen_checksums.go 重新生成 checksums.golden。
//
// 从本目录执行：
//
//	go run gen_checksums.go
//
// 或从仓库根目录执行 scripts/gen-migration-checksums.sh。
//
// 只在新增迁移时运行。已应用的迁移不得修改；改了 up 文件再重生成 golden，
// 只会让清单与线上 schema_migrations 里的旧 checksum 对不上。
package main

import (
	"bufio"
	"fmt"
	"os"

	"yunshu.dev/core/internal/platform/migrate"
)

const (
	goldenName = "checksums.golden"
	header     = `# 已交付迁移的 up 文件 SHA-256 清单，每行 "<version> <sha256>"，按 version 升序。
# 测试 TestEmbeddedMigrationsMatchGoldenChecksums 逐条比对，任何一字节改动都会变红。
#
# 已应用的迁移不得修改；如果确实需要改，请新增迁移并在 golden 里重生成：
#   bash scripts/gen-migration-checksums.sh
#
# 0007 / 0008 为保留版本号，不列出。
`
)

func main() {
	// 挡住“在仓库根目录误跑”的情况：生成结果必须落在包目录内。
	if _, err := os.Stat(migrate.MySQLDir); err != nil {
		fmt.Fprintf(os.Stderr, "当前目录没有 %s，请先 cd internal/platform/migrate: %v\n", migrate.MySQLDir, err)
		os.Exit(1)
	}

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载 embedded 迁移失败: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Create(goldenName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 %s 失败: %v\n", goldenName, err)
		os.Exit(1)
	}
	w := bufio.NewWriter(f)
	if _, err := w.WriteString(header); err != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", goldenName, err)
		os.Exit(1)
	}
	for _, m := range migrations {
		if _, err := fmt.Fprintf(w, "%s %s\n", m.Version, m.Checksum); err != nil {
			fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", goldenName, err)
			os.Exit(1)
		}
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", goldenName, err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "关闭 %s 失败: %v\n", goldenName, err)
		os.Exit(1)
	}

	fmt.Printf("已生成 %s：%d 条迁移（%s - %s）\n",
		goldenName, len(migrations), migrations[0].Version, migrations[len(migrations)-1].Version)
}
