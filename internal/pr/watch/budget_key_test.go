package watch

import "testing"

// applyCI used to hide a budget failure with a prefix check. The typed key
// must accept that family and reject a near miss, including the notice family.
func TestParseBudgetFailureKeyGuardsFamily(t *testing.T) {
	key := NewBudgetFailureKey("abc", "81/1")
	if key.String() != "budget:abc:81/1" {
		t.Fatalf("key = %q", key.String())
	}
	parsed, ok := ParseBudgetFailureKey(key.String())
	if !ok || parsed.Head() != "abc" || parsed.Token() != "81/1" || parsed.String() != key.String() {
		t.Fatalf("parse = %#v, %v", parsed, ok)
	}
	if !BudgetFailureNamesHead(key.String(), "abc") || BudgetFailureNamesHead(key.String(), "ab") {
		t.Fatal("head match changed")
	}
	// A bare family prefix still counts, as the old prefix check did.
	if _, ok := ParseBudgetFailureKey("budget:"); !ok {
		t.Fatal("bare family prefix must stay a budget key")
	}
	for _, raw := range []string{"", "budget", "budgetish:abc:81/1", "ci-budget:abc:81/1", "test-budget:abc:81/1"} {
		if _, ok := ParseBudgetFailureKey(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}
