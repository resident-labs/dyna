package watch

import (
	"strings"
	"testing"
)

// D-262: a reviewer wrote review672.md into a literal "$DYNA_SCRATCH" folder
// of the author's worktree, because a file tool does not expand variables.
func TestReviewTaskKeepsReviewerOutOfAuthorCheckout(t *testing.T) {
	got := reviewTask(Record{Kind: "local", Key: "change_1", Head: "abc"})
	for _, want := range []string{"never write files there", `echo "$DYNA_SCRATCH"`} {
		if !strings.Contains(got, want) {
			t.Errorf("review task lacks %q", want)
		}
	}
}
