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

func TestLoweredCapKeepsAcceptedRules(t *testing.T) {
	s := testStore(t)
	for range 3 {
		if _, err := s.AddRule("room", "owner", "owner", strings.Repeat("x", 500)); err != nil {
			t.Fatal(err)
		}
	}
	setRulesCap(t, 500)
	sections := s.InstructionSections("room")
	if got := sections[1].Items; len(got) != 3 || strings.Contains(strings.Join(got, "\n"), "more: dyna rules") {
		t.Fatalf("lowered cap dropped accepted rules: %q", got)
	}
}
