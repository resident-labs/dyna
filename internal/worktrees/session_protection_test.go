package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
)

func TestSessionProtection(t *testing.T) {
	tree := inferenceTestStore(t)
	recordtest.Write(t, runstore.SessionsDir(), "worker", map[string]any{
		"id": "worker", "room": "project", "dir": tree,
	})
	rec := Record{Path: tree}
	protected, err := SessionProtected(context.Background(), rec)
	if err != nil || !protected {
		t.Fatalf("open inferred session: protected=%v, err=%v", protected, err)
	}
	recordtest.Write(t, runstore.SessionsDir(), "worker", map[string]any{
		"id": "worker", "room": "project", "dir": tree, "closed": time.Now(),
	})
	if _, err := goals.DefaultStore().Add("project", "setter", goals.Input{Text: "Finish work", Holders: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	protected, err = SessionProtected(context.Background(), rec)
	if err != nil || !protected {
		t.Fatalf("closed session with open goal: protected=%v, err=%v", protected, err)
	}
}

// A run disposition calls Remove directly, bypassing the service's guard.
func TestRunDispositionKeepsOpenSessionTree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	tree, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tree) })
	rec := Record{Path: tree, Repo: filepath.Join(t.TempDir(), "missing"), RunID: "wf_shared"}
	if err := Track(rec); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, runstore.SessionsDir(), "worker", map[string]any{"id": "worker", "room": "project", "dir": tree})
	_, err = ApplyRunDisposition("wf_shared", DispositionRemoveAll)
	if err == nil || !strings.Contains(err.Error(), "open session") {
		t.Fatalf("expected open session protection, got %v", err)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("tree removed: %v", err)
	}
}
