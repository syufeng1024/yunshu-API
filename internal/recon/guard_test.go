package recon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

// TestCheckRegistryMatchesSettlementSection2 pins the Daily.Checks() surface:
// R-01..R-08 plus R09_CONFIG_TRACE. Adding, removing or reordering a check also
// changes cmd/recondrill's per-assertion output and the 实现 column of
// docs/settlement.md §2, so both must move in the same change.
func TestCheckRegistryMatchesSettlementSection2(t *testing.T) {
	var d Daily
	var got []string
	for _, c := range d.Checks() {
		got = append(got, c.Code)
	}
	want := []string{
		DiffR01, DiffR02, DiffR03, DiffR04, DiffR05, DiffR06, DiffR07, DiffR08,
		checkCodeR09,
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Checks() = %v; want %v", got, want)
	}
	t.Logf("checks registered: %s", strings.Join(got, ","))
}

// TestDiffTypeEnumStaysFrozen keeps the diff_type enum at 8 D-19 internal values
// plus the 4 frozen TDS §5.4.1 upstream values. A settlement §2 assertion that
// needs a new label gets a check code (R09_CONFIG_TRACE) and reuses one of the
// existing values, so this count must stay at 12.
func TestDiffTypeEnumStaysFrozen(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package: %v", err)
	}
	values := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range vs.Names {
					if !strings.HasPrefix(ident.Name, "Diff") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: unquote %s: %v", name, lit.Value, err)
					}
					values[ident.Name] = value
				}
			}
		}
	}
	const wantCount = 12 // 8 internal D-19 + 4 frozen external
	if len(values) != wantCount {
		t.Fatalf("diff_type constants = %d (%v); want %d; reuse an existing value and add a check code instead",
			len(values), values, wantCount)
	}
	for name, want := range map[string]string{
		"DiffTokenMismatch":   "TOKEN_MISMATCH",
		"DiffAmountMismatch":  "AMOUNT_MISMATCH",
		"DiffMissingLocal":    "MISSING_LOCAL",
		"DiffMissingUpstream": "MISSING_UPSTREAM",
	} {
		if values[name] != want {
			t.Fatalf("%s = %q; want %q (frozen TDS §5.4.1 value)", name, values[name], want)
		}
	}
	t.Logf("diff_type constants=%d; frozen external values unchanged", len(values))
}
