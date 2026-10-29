package goals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/settings"
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

func TestInstructionsKeepRulesWhenSettingsAreBroken(t *testing.T) {
	s := testStore(t)
	if _, err := s.AddRule("room", "owner", "owner", "Run tests."); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(settings.DefaultPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.DefaultPath(), []byte(`{"rules":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.InstructionSections("room")[1].Items; len(got) != 1 || !strings.HasPrefix(got[0], "r1 Run tests.") {
		t.Fatalf("broken settings hid accepted rules: %q", got)
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
