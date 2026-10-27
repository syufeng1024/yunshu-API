package acceptance

// T21 A12: docs/test-matrix.md is the audit handover document. Every test it
// names must exist, and the 14 CI / 12 GWT counts must match the source. A
// renamed test therefore breaks CI instead of leaving a stale matrix.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var (
	testRowName = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	testFuncRE  = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func sourceTests(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "web", "node_modules", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range testFuncRE.FindAllStringSubmatch(string(raw), -1) {
			// TestMain and a few generic names may repeat per package; the
			// matrix only needs to know that a documented name exists.
			if _, exists := found[m[1]]; !exists {
				found[m[1]] = path
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk tests: %v", err)
	}
	return found
}

func docTests(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "docs", "test-matrix.md")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open docs/test-matrix.md: %v", err)
	}
	defer f.Close()

	seen := map[string]bool{}
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "|") {
			continue
		}
		for _, m := range testRowName.FindAllStringSubmatch(line, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read docs/test-matrix.md: %v", err)
	}
	sort.Strings(names)
	return names
}

func TestMatrixNamesExistAndCountsMatch(t *testing.T) {
	root := repoRoot(t)
	source := sourceTests(t, root)
	doc := docTests(t, root)

	if len(doc) == 0 {
		t.Fatal("docs/test-matrix.md lists no test names")
	}
	for _, name := range doc {
		if _, ok := source[name]; !ok {
			t.Fatalf("docs/test-matrix.md names %s, but no _test.go defines it", name)
		}
	}

	countPrefix := func(names []string, prefix string) int {
		n := 0
		for _, name := range names {
			if strings.HasPrefix(name, prefix) {
				n++
			}
		}
		return n
	}

	docCI, docGWT := countPrefix(doc, "TestCI_"), countPrefix(doc, "TestGWT")
	srcCI, srcGWT := 0, 0
	for name := range source {
		if strings.HasPrefix(name, "TestCI_") {
			srcCI++
		}
		if strings.HasPrefix(name, "TestGWT") {
			srcGWT++
		}
	}
	if docCI != 14 || srcCI != 14 {
		t.Fatalf("14 CI items: doc=%d source=%d, want 14", docCI, srcCI)
	}
	if docGWT != 12 || srcGWT != 12 {
		t.Fatalf("12 GWT items: doc=%d source=%d, want 12", docGWT, srcGWT)
	}
	t.Logf("matrix: %d documented tests; TestCI_=%d; TestGWT=%d", len(doc), docCI, docGWT)
}
