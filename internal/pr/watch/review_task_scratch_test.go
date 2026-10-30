package watch

import (
	"strings"
	"testing"
)

// D-262: a reviewer wrote review672.md into a literal "$DYNA_SCRATCH" folder
// of the author's worktree, because a file tool does not expand variables.
func TestReviewTaskKeepsReviewerOutOfAuthorCheckout(t *testing.T) {
	got := reviewTask(Record{Kind: "local", Key: "change_1", Head: "abc"})
	for _, want := range []string{"Write all files under your scratch folder", `echo "$DYNA_SCRATCH"`} {
		if !strings.Contains(got, want) {
			t.Errorf("review task lacks %q", want)
		}
	}
}

func TestGitHubReviewTaskPrefersPreparedDiff(t *testing.T) {
	got := reviewTask(Record{Kind: "github", Key: "github.com/owner/repo#17", Repo: "owner/repo", Number: 17, Head: "abc"})
	if !strings.Contains(got, "prepared diff") {
		t.Errorf("task does not direct the reviewer to the prepared diff: %s", got)
	}
	if !strings.Contains(got, "If that file is missing") {
		t.Errorf("task has no gh pr diff fallback: %s", got)
	}
}
