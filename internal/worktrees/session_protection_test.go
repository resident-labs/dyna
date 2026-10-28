package worktrees

import (
	"context"
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
