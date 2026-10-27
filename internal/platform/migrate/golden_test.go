package migrate

// T21 A10 第二层：已交付迁移的字节清单。
//
// TestAppliedMigrationChecksumTamperIsRejected 证明引擎会拒绝“库里已有记录”的
// 篡改，但它用的是 fstest.MapFS 自造 fixture，改真实文件仍不会变红。生产上的
// 真实事故是：有人给已应用的 0003 加一行注释，本地和 CI 都绿，直到生产执行
// migrate up 才因为 checksum 不匹配炸掉。
//
// 本用例直接拿 embedded 的 up 文件与 checksums.golden 逐条比对，不维护任何
// 硬编码版本列表：新增迁移而忘了重生成清单同样失败，这是预期行为。

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
)

const checksumGoldenFile = "checksums.golden"

// readChecksumGolden 解析 "<version> <sha256>" 行，忽略 # 注释与空行。
// 重复版本或乱序同样视为清单损坏，避免重生成后出现难以阅读的 diff。
func readChecksumGolden(path string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 %s 失败: %w", path, err)
	}
	defer f.Close()

	golden := make(map[string]string)
	var order []string
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, nil, fmt.Errorf("%s 第 %d 行格式不是 \"<version> <sha256>\": %q", path, lineNo, line)
		}
		version, checksum := fields[0], fields[1]
		if _, exists := golden[version]; exists {
			return nil, nil, fmt.Errorf("%s 第 %d 行版本 %s 重复", path, lineNo, version)
		}
		if len(version) != 4 || len(checksum) != 64 {
			return nil, nil, fmt.Errorf("%s 第 %d 行版本或 sha256 长度不合法: %q", path, lineNo, line)
		}
		if len(order) > 0 && version <= order[len(order)-1] {
			return nil, nil, fmt.Errorf("%s 第 %d 行版本 %s 未按升序排列（前一条 %s）", path, lineNo, version, order[len(order)-1])
		}
		golden[version] = checksum
		order = append(order, version)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if len(order) == 0 {
		return nil, nil, fmt.Errorf("%s 中没有任何清单行", path)
	}
	return golden, order, nil
}

func TestEmbeddedMigrationsMatchGoldenChecksums(t *testing.T) {
	migrations, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("加载 embedded 迁移失败: %v", err)
	}

	golden, order, err := readChecksumGolden(checksumGoldenFile)
	if err != nil {
		t.Fatalf("%v\n提示：运行 bash scripts/gen-migration-checksums.sh 重生成清单", err)
	}

	embedded := make(map[string]Migration, len(migrations))
	for _, m := range migrations {
		embedded[m.Version] = m
	}

	for _, m := range migrations {
		want, ok := golden[m.Version]
		if !ok {
			t.Errorf("迁移 %s（版本 %s）不在 %s 中。\n"+
				"新增迁移后必须显式重生成清单：bash scripts/gen-migration-checksums.sh",
				m.UpFile, m.Version, checksumGoldenFile)
			continue
		}
		if want != m.Checksum {
			t.Errorf("迁移文件被修改：%s（版本 %s）\n"+
				"  期望: %s\n"+
				"  实际: %s\n"+
				"已应用的迁移不得修改；如果确实需要改，请新增迁移并在 golden 里重生成（bash scripts/gen-migration-checksums.sh）",
				m.UpFile, m.Version, want, m.Checksum)
		}
	}

	for _, version := range order {
		if _, ok := embedded[version]; !ok {
			t.Errorf("%s 列出的版本 %s 在 embedded 迁移中不存在。\n"+
				"删除或改名迁移文件后也要重生成清单：bash scripts/gen-migration-checksums.sh",
				checksumGoldenFile, version)
		}
	}
}
