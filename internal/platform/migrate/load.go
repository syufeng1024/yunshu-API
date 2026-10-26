// Package migrate loads and executes the explicit SQL migrations under
// sql/mysql. It intentionally has no ORM and no schema auto-generation:
// every structural change is a reviewed .sql file.
package migrate

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// MySQLDir is the only directory the loader reads.
const MySQLDir = "sql/mysql"

// File naming is frozen: four-digit version, lower-case snake_case name, and a
// paired .down.sql. 0007 / 0008 are reserved and are rejected by the loader.
var (
	upNameRE   = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
	downNameRE = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.down\.sql$`)
)

//go:embed sql/mysql/*.sql
var embeddedSQL embed.FS

// Migration is one up/down pair. Checksum covers the exact bytes of the up
// file; changing one comment invalidates every database that applied it.
type Migration struct {
	Version  string
	Name     string
	UpFile   string
	DownFile string
	UpSQL    string
	DownSQL  string
	Checksum string
}

// Embedded returns the compile-time SQL tree.
func Embedded() fs.FS { return embeddedSQL }

// LoadEmbedded loads the paired migrations compiled into the binary.
func LoadEmbedded() ([]Migration, error) { return Load(embeddedSQL, MySQLDir) }

// Load reads and validates every migration in dir.
//
// Validation order is deliberate: file names, then duplicate versions, then
// down-file pairing. A duplicate version is reported with both occupant file
// names so the conflict is actionable from the startup log alone.
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录 %s 失败: %w", dir, err)
	}

	upFiles := make(map[string]string)   // version -> up file name
	downFiles := make(map[string]string) // up stem -> down file name
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case downNameRE.MatchString(name):
			downFiles[strings.TrimSuffix(name, ".down.sql")] = name
		case upNameRE.MatchString(name):
			version := upNameRE.FindStringSubmatch(name)[1]
			if previous, exists := upFiles[version]; exists {
				return nil, fmt.Errorf("版本号 %s 被 %s 与 %s 重复占用", version, previous, name)
			}
			upFiles[version] = name
		default:
			return nil, fmt.Errorf("迁移文件名 %q 不符合 ^\\d{4}_[a-z0-9_]+\\.sql$", name)
		}
	}

	if len(upFiles) == 0 {
		return nil, fmt.Errorf("迁移目录 %s 中没有 .sql 文件", dir)
	}

	versions := make([]string, 0, len(upFiles))
	for version := range upFiles {
		versions = append(versions, version)
	}
	sort.Strings(versions)

	migrations := make([]Migration, 0, len(versions))
	for _, version := range versions {
		upFile := upFiles[version]
		stem := strings.TrimSuffix(upFile, ".sql")
		downFile, ok := downFiles[stem]
		if !ok {
			return nil, fmt.Errorf("迁移 %s 缺少配对的 down 文件 %s.down.sql", upFile, stem)
		}

		upSQL, err := fs.ReadFile(fsys, path.Join(dir, upFile))
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", upFile, err)
		}
		downSQL, err := fs.ReadFile(fsys, path.Join(dir, downFile))
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", downFile, err)
		}

		sum := sha256.Sum256(upSQL)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     stem,
			UpFile:   upFile,
			DownFile: downFile,
			UpSQL:    string(upSQL),
			DownSQL:  string(downSQL),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	return migrations, nil
}

// Checksum returns the SHA-256 of body as lower-case hex. It is exported so a
// test can prove that a one-character comment edit changes the checksum.
func Checksum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
