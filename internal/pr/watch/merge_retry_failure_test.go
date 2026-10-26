package watch

import (
	"strings"
	"testing"
)

func TestRetryRebuildFailureNamesEachChangeAndSuggestsResume(t *testing.T) {
	got := retryRebuildFailure([]MergeQueueItem{
		{URL: "https://github.com/o/r/pull/662", State: "ready"},
		{URL: "https://github.com/o/r/pull/670", State: "waiting", Reason: "waiting for CI"},
	})
	for _, want := range []string{"pull/662 is ready", "pull/670 is waiting: waiting for CI", "dyna git merge resume"} {
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
