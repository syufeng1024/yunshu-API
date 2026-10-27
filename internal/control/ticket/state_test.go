package ticket

import "testing"

// Hard constraint 1: the ticket state machine has exactly three values.
// This test pins the enumeration and, more importantly, proves that a filter
// naming any fourth value is rejected instead of quietly matching nothing.
func TestTicketStateEnumHasExactlyThreeValues(t *testing.T) {
	states := []string{StateOpen, StateResponded, StateClosed}
	if len(states) != 3 {
		t.Fatalf("state count=%d, want 3", len(states))
	}
	seen := map[string]bool{}
	for _, s := range states {
		if seen[s] {
			t.Fatalf("duplicate state %q", s)
		}
		seen[s] = true
		if got, apiErr := parseStateFilter(s); apiErr != nil || got != s {
			t.Fatalf("parseStateFilter(%q)=%q,%v", s, got, apiErr)
		}
	}

	// A fourth state — auto-closed or otherwise — is not a filter value.
	for _, invented := range []string{"PENDING", "REOPENED", "CLOSED_BY_SYSTEM"} {
		if _, apiErr := parseStateFilter(invented); apiErr == nil {
			t.Fatalf("state filter accepted the invented value %q", invented)
		}
	}
}

// The four ticket types are also a closed enum; a fifth type is a body error.
func TestTicketTypeEnumHasExactlyFourValues(t *testing.T) {
	types := []string{TypeBilling, TypeTech, TypeContentAppeal, TypeOther}
	if len(types) != 4 {
		t.Fatalf("type count=%d, want 4", len(types))
	}
	for _, typ := range types {
		if !validType(typ) {
			t.Fatalf("validType(%q)=false", typ)
		}
	}
	if validType("FEATURE_REQUEST") {
		t.Fatal("validType accepted a fifth ticket type")
	}
}
