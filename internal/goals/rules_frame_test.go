package goals

import (
	"strings"
	"testing"
)

func TestRuleLines(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Text: "Keep\n\ncode\tsimple.", Setter: "owner"},
		{ID: "r2", Text: "Run tests.", Setter: "session", SetterName: "team:tester\nname"},
	}
	got := strings.Join(RuleLines(rules), "\n")
	for _, want := range []string{"r1 Keep code simple. · owner", "r2 Run tests. · agent team:tester name"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("rule text forged a line: %q", got)
	}
	if len(RuleLines(nil)) != 0 {
		t.Fatal("empty rules added a line")
	}
}

func TestRuleLinesGuard(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Text: strings.Repeat("界", MaxRulesText), Setter: "owner"},
		{ID: "r2", Text: "overflow", Setter: "owner"},
		{ID: "r3", Text: "also omitted", Setter: "owner"},
	}
	got := strings.Join(RuleLines(rules), "\n")
	if !strings.Contains(got, rules[0].Text) || !strings.HasSuffix(got, "+2 more: dyna rules") || strings.Contains(got, "overflow") {
		t.Fatal("guard did not count runes or omitted rules")
	}
	rules[0].Text += "界"
	if got := strings.Join(RuleLines(rules), "\n"); !strings.HasSuffix(got, "+3 more: dyna rules") {
		t.Fatal(got)
	}
}
