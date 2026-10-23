package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/runstore"
)

// Owner lookup reads a run's small worker directory list, never its journals.
// The journals hold every prompt and result and made each scan take seconds.
func TestInferOwnersReadsRunDirectoryList(t *testing.T) {
	tree := inferenceTestStore(t)
	run := filepath.Join(runstore.RunsDir(), "wf_listed")
	inferenceTestWrite(t, filepath.Join(run, "meta.json"), map[string]any{"id": "wf_listed", "status": "running"})
	inferenceTestBytes(t, filepath.Join(run, "dirs.jsonl"), []byte(`"`+tree+`"`+"\n"))
	// Finish the one-time import before testing runtime list lookup.
	if err := runstore.Migrate(filepath.Dir(runstore.RunsDir())); err != nil {
		t.Fatal(err)
	}
	// The journal is not read: invalid content would fail a journal read.
	inferenceTestBytes(t, filepath.Join(run, "journal.jsonl"), []byte("not json\n"))
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[tree].Runs) != 1 || got[tree].Runs[0].ID != "wf_listed" || got[tree].Runs[0].Closed {
		t.Fatalf("owners = %#v", got[tree])
	}
}

// Migration imports old journals; cleanup does not rebuild legacy lists.
func TestInferOwnersDoesNotRebuildLegacyList(t *testing.T) {
	tree := inferenceTestStore(t)
	for _, run := range []struct {
		id    string
		meta  map[string]any
		write bool
	}{
		{"wf_ended", map[string]any{"id": "wf_ended", "status": "ok", "endedAt": "2026-01-01T00:00:00Z"}, true},
		{"wf_running", map[string]any{"id": "wf_running", "status": "running"}, false},
	} {
		dir := filepath.Join(runstore.RunsDir(), run.id)
		inferenceTestWrite(t, filepath.Join(dir, "meta.json"), run.meta)
		inferenceTestWrite(t, filepath.Join(dir, "journal.jsonl"), map[string]any{"id": 1, "cwd": tree})
	}
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil || len(got[tree].Runs) != 2 {
		t.Fatalf("owners = %#v, %v", got[tree], err)
	}
	if _, err := os.Stat(filepath.Join(runstore.RunsDir(), "wf_ended", "dirs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("cleanup rebuilt ended legacy run list: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runstore.RunsDir(), "wf_running", "dirs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("running legacy run got a list: %v", err)
	}
}
