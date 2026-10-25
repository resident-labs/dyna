package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	harnesshome "dyna-agent/internal/harness/home"
)

// Global rules travel in each run's instructions now. A generation removes
// the instruction file older Dyna versions wrote into the account home, and
// leaves a file the harness or the user wrote.
func TestGenerationRemovesLegacyGlobalRulesFiles(t *testing.T) {
	targets := map[string]string{
		"claude-code": "CLAUDE.md",
		"opencode":    "AGENTS.md",
		"pi":          "AGENTS.md",
		"muse":        "~/.config/muse/AGENTS.md",
	}
	for _, a := range RuntimeAdapters() {
		if _, ok := targets[a.Harness]; !ok {
			t.Errorf("%s has no expected legacy global rules file in this test", a.Harness)
		}
	}
	for h, rel := range targets {
		t.Run(h, func(t *testing.T) {
			r, home, id := generationFixture(t, h)
			target := filepath.Join(home, rel)
			if after, ok := strings.CutPrefix(rel, "~/"); ok {
				target = filepath.Join(harnesshome.Home(home), after)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(goals.GlobalRulesMarker+"\n# Dyna global rules\n- u1 Old rule.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			buildGeneration(t, r, h, home, id)
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("the legacy Dyna file %s survived: %v", target, err)
			}
			if err := os.WriteFile(target, []byte("written by the harness\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			buildGeneration(t, r, h, home, id)
			if raw, _ := os.ReadFile(target); string(raw) != "written by the harness\n" {
				t.Fatalf("cleanup changed a file Dyna did not write: %q", raw)
			}
		})
	}
}
