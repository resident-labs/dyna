package librarian

import (
	"strings"
	"testing"
)

func TestTaskStatesMaintainModeAndTheReadmeGate(t *testing.T) {
	task := Task("/src/app", "/data/tree", "aaa", "bbbbbbbbbbbbbbbb", "owned file x.go changed", 120)
	for _, want := range []string{
		"Correct only statements that are now false. Leave true statements, wording and structure alone",
		"dyna library check-readme",
		"(120 lines)",
		"do not open a pull request",
		"dyna git pr track",
		"Never merge",
		"dyna library changed --since aaa",
		"Re-verify every stale entry, not only the ones this change touched",
		"ask whether it adds a sub-feature",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("task lacks %q", want)
		}
	}
}
