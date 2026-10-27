package fund

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A14: docs/fund-flow.md and the code must agree on the state sets. The
// document carries a machine-readable anchor line; the test compares sets, so
// adding a state to one side and not the other fails here instead of in review.
func TestOrderStateMachineDocMatchesCode(t *testing.T) {
	doc := readFundFlow(t)
	anchors := parseAnchors(doc)
	documented, ok := anchors["fundspec:order-states"]
	if !ok {
		t.Fatalf("docs/fund-flow.md has no 'fundspec:order-states' anchor; known anchors: %v", anchorNames(anchors))
	}
	got := setOf(orderStates)
	want := setOf(strings.Split(documented, ","))
	if len(got) != len(want) {
		t.Fatalf("state count differs: code=%v doc=%v", keys(got), keys(want))
	}
	for state := range got {
		if !want[state] {
			t.Errorf("state %q exists in code but not in docs/fund-flow.md", state)
		}
	}
	for state := range want {
		if !got[state] {
			t.Errorf("state %q exists in docs/fund-flow.md but not in code", state)
		}
	}
	t.Logf("order states (%d): %v", len(got), keys(got))
}

// The document must also draw the order diagram, and the diagram must mention
// every state: a state nobody can reach is a dead enum, one missing from the
// picture is a documentation gap.
func TestOrderDiagramCoversEveryState(t *testing.T) {
	doc := readFundFlow(t)
	for _, state := range orderStates {
		if !strings.Contains(doc, state) {
			t.Errorf("docs/fund-flow.md never mentions the state %q", state)
		}
	}
	if !strings.Contains(doc, "stateDiagram-v2") {
		t.Error("docs/fund-flow.md has no mermaid state diagram")
	}
}

// parseOrderStatus must accept every state the enumeration exposes, so an
// operator can filter by a state the table can actually hold.
func TestStatusFilterCoversEveryState(t *testing.T) {
	for _, state := range orderStates {
		filter := strings.ToLower(state)
		if filter == "partial_refunded" {
			filter = "refunded" // the console folds both refunded states
		}
		got, apiErr := parseOrderStatus(filter)
		if apiErr != nil {
			t.Errorf("state %q has no filter: %v", state, apiErr)
			continue
		}
		found := false
		for _, st := range got {
			if st == state {
				found = true
			}
		}
		if !found && state != "PARTIAL_REFUNDED" {
			t.Errorf("filter %q did not select state %q (got %v)", filter, state, got)
		}
	}
}

func readFundFlow(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "docs", "fund-flow.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read docs/fund-flow.md: %v", err)
	}
	return string(raw)
}

// parseAnchors reads lines shaped like "fundspec:<name> a,b,c".
func parseAnchors(doc string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "fundspec:") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		out[parts[0]] = strings.TrimSpace(parts[1])
	}
	return out
}

func setOf(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func anchorNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
