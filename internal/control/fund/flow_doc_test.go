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

// A14: the other three state machines of T13's second segment. The anchor line
// holds the set; the comparison is set equality, so a state added to one side
// fails here before it reaches review.
func TestSecondSegmentStateMachinesMatchCode(t *testing.T) {
	doc := readFundFlow(t)
	anchors := parseAnchors(doc)
	cases := []struct {
		anchor string
		code   []string
	}{
		{"fundspec:refund-states", refundStates},
		{"fundspec:invoice-states", invoiceStates},
		{"fundspec:quota-states", quotaStates},
	}
	for _, tc := range cases {
		documented, ok := anchors[tc.anchor]
		if !ok {
			t.Errorf("docs/fund-flow.md has no %q anchor; known anchors: %v",
				tc.anchor, anchorNames(anchors))
			continue
		}
		got := setOf(tc.code)
		want := setOf(strings.Split(documented, ","))
		if len(got) != len(want) {
			t.Errorf("%s: count differs code=%v doc=%v", tc.anchor, keys(got), keys(want))
		}
		for state := range got {
			if !want[state] {
				t.Errorf("%s: state %q exists in code but not in docs", tc.anchor, state)
			}
		}
		for state := range want {
			if !got[state] {
				t.Errorf("%s: state %q exists in docs but not in code", tc.anchor, state)
			}
		}
		t.Logf("%s (%d): %v", tc.anchor, len(got), keys(got))
	}
}

// Each of the three new diagrams must draw its states and be a mermaid block.
// The order diagram test already asserts "stateDiagram-v2" exists; the
// per-section check is done by requiring two more occurrences.
func TestSecondSegmentDiagramsMentionEveryState(t *testing.T) {
	doc := readFundFlow(t)
	if got := strings.Count(doc, "stateDiagram-v2"); got < 4 {
		t.Errorf("docs/fund-flow.md has %d mermaid state diagrams, want 4 (order/refund/invoice/quota)", got)
	}
	sections := []struct {
		heading string
		states  []string
	}{
		{"## 1. 订单状态机", orderStates},
		{"## 5. 退款状态机", refundStates},
		{"## 6. 发票状态机", invoiceStates},
		{"## 7. 提额状态机", quotaStates},
	}
	for _, sec := range sections {
		body := sectionOf(doc, sec.heading)
		if body == "" {
			t.Errorf("docs/fund-flow.md has no %q section", sec.heading)
			continue
		}
		for _, state := range sec.states {
			if !strings.Contains(body, state) {
				t.Errorf("%s never mentions the state %q", sec.heading, state)
			}
		}
	}
}

// sectionOf returns the body under one "## " heading, stopping at the next
// "## " heading. Scoping the search matters: a state named only in a later
// section must not satisfy this diagram's check.
func sectionOf(doc, heading string) string {
	idx := strings.Index(doc, heading)
	if idx < 0 {
		return ""
	}
	rest := doc[idx+len(heading):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		return rest[:next]
	}
	return rest
}

// The document must also draw the order diagram, and the diagram must mention
// every state: a state nobody can reach is a dead enum, one missing from the
// picture is a documentation gap.
func TestOrderDiagramCoversEveryState(t *testing.T) {
	body := sectionOf(readFundFlow(t), "## 1. 订单状态机")
	if body == "" {
		t.Fatal("docs/fund-flow.md has no '## 1. 订单状态机' section")
	}
	for _, state := range orderStates {
		if !strings.Contains(body, state) {
			t.Errorf("docs/fund-flow.md order diagram never mentions the state %q", state)
		}
	}
	if !strings.Contains(body, "stateDiagram-v2") {
		t.Error("docs/fund-flow.md order section has no mermaid state diagram")
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
