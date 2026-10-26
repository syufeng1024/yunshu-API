package recon

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guard rails that must hold in production code, not just in this package:
//
//   - no UPDATE of the materialised balance (all money moves through ledger)
//   - no UPDATE/DELETE of t_ledger_entry (append-only)
//   - no DROP PARTITION / TRUNCATE in the retention task
//
// _test.go files are excluded: fixtures may clean up their own rows.
func TestProductionSQLGuardRails(t *testing.T) {
	root := filepath.Join("..", "..")
	forbidden := []struct {
		name    string
		pattern string
	}{
		{"balance overwrite", "UPDATE T_CREDIT_ACCOUNT SET BALANCE_MICRO"},
		{"ledger update", "UPDATE T_LEDGER_ENTRY"},
		{"ledger delete", "DELETE FROM T_LEDGER_ENTRY"},
		{"partition drop", "DROP PARTITION"},
		{"table truncate", "TRUNCATE TABLE"},
	}
	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "docs", "dist", "build":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		upper := strings.ToUpper(string(raw))
		for _, f := range forbidden {
			if strings.Contains(upper, f.pattern) {
				violations = append(violations, f.name+": "+path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("forbidden SQL in production code:\n%s", strings.Join(violations, "\n"))
	}
	t.Logf("guard rails: scanned %s; no balance overwrite, ledger UPDATE/DELETE, DROP PARTITION or TRUNCATE", root)
}
