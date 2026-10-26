package watch

import (
	"strings"
	"testing"
)

func TestRetryRebuildFailureNamesEachChangeAndSuggestsResume(t *testing.T) {
	got := retryRebuildFailure([]MergeQueueItem{
		{URL: "https://github.com/o/r/pull/662", State: "ready"},
		{URL: "https://github.com/o/r/pull/670", State: "waiting", Reason: "waiting for CI"},
		{ChangeID: "change_a1", Title: "Fix order", State: "ready"},
	})
	for _, want := range []string{"pull/662 is ready", "pull/670 is waiting: waiting for CI", `"Fix order" (change_a1) is ready`, "dyna git merge resume"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "no eligible PRs") {
		t.Errorf("message keeps the old text: %q", got)
	}
	if empty := retryRebuildFailure(nil); !strings.Contains(empty, "dyna git merge resume") {
		t.Errorf("empty queue message %q", empty)
	}
}
