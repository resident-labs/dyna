package librarian

import (
	"strings"
	"testing"
)

func TestSmellTaskReadsCodeWithAChecklist(t *testing.T) {
	task := SmellTask("/src/app", "/data/smell-tree", "aaa", "bbbbbbbbbbbbbbbb", 10)
	for _, want := range []string{
		"git diff --name-only aaa..bbbbbbbbbbbbbbbb",
		"Read each full changed file",
		"callers",
		"sibling features",
		"Raw system text in the UI",
		"map[string]any",
		"Decisions made by parsing address strings",
		"invented senders",
		"leftovers from removed features",
		"oversized files",
		"dyna library smell",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("smell task lacks %q", want)
		}
	}
	if strings.Contains(Task("/src/app", "github.com/fork/app", "/data/tree", "aaa", "bbb", "change", 120), "3. Smells.") {
		t.Error("map task still includes smell finding")
	}
}

func TestTaskStatesMaintainModeAndTheReadmeGate(t *testing.T) {
	task := Task("/src/app", "github.com/fork/app", "/data/tree", "aaa", "bbbbbbbbbbbbbbbb", "owned file x.go changed", 120)
	for _, want := range []string{
		"Correct only statements that are now false. Preserve true statements, wording and structure",
		"dyna library check-readme",
		"(120 lines)",
		"leave the pull request unopened",
		"Never push to an existing remote branch",
		"gh pr create --repo github.com/fork/app --body-file FILE",
		"dyna git pr track",
		"Leave merging to the merge process",
		"dyna library changed --since aaa",
		"Re-verify every stale entry, including entries outside this change",
		"ask whether it adds a sub-feature",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("task lacks %q", want)
		}
	}
}
