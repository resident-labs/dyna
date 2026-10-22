package watch

import (
	"os"
	"path/filepath"
	"testing"
)

// Trial CI must run the same workflow as main, while ordinary feature pushes
// remain covered by their pull-request runs.
func TestRepositoryWorkflowRunsOnMergeTrials(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := workflowEvents(body)
	if err != nil {
		t.Fatal(err)
	}
	for branch, want := range map[string]bool{"main": true, "dyna/merge-trial/1": true, "feature/example": false} {
		got, err := pushMatchesBranch(events["push"], branch)
		if err != nil || got != want {
			t.Errorf("push to %s: got %v, %v; want %v", branch, got, err, want)
		}
	}
}
