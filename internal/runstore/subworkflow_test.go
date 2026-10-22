package runstore

import (
	"os"
	"path/filepath"
	"testing"
)

// A sub-workflow keeps its script, logs and result, and no meta.json: no
// reader used it, and the parent run's events carry its name and status.
func TestSubWorkflowWritesNoMeta(t *testing.T) {
	run := newTestRun(t, "wf_sub-meta")
	if err := run.StartWorkflow("nested-1", "return 1"); err != nil {
		t.Fatal(err)
	}
	if err := run.FinishWorkflow("nested-1", "1"); err != nil {
		t.Fatal(err)
	}
	dir := run.workflowDir("nested-1")
	for _, name := range []string{"script.js", "events.jsonl", "journal.jsonl", "result.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("meta.json: %v; want none", err)
	}
}

// Starting the same sub-workflow twice fails, as it did when its logs were
// created exclusively.
func TestSubWorkflowStartsOnce(t *testing.T) {
	run := newTestRun(t, "wf_sub-twice")
	if err := run.StartWorkflow("nested-1", "return 1"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartWorkflow("nested-1", "return 2"); err == nil {
		t.Fatal("a second start of nested-1 succeeded")
	}
}
