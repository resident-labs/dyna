package hooks

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/room"
)

// A new worktree's hook finishes before the call returns, so the caller can
// report its failure and name the hook that failed.
func TestRunWorktreeCreatedWaitsAndNamesFailure(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("seed", WorktreeCreated))
	tree := filepath.Join(t.TempDir(), "tree")
	git(t, repo, "worktree", "add", "-b", "feature", tree)
	var ran int
	s.Post = func(Event, room.HookEvent) error { return nil }
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		ran++
		return errors.New("seed exploded")
	}
	err := s.RunWorktreeCreated(context.Background(), Event{Repo: tree, Worktree: tree, Branch: "feature"})
	if ran != 1 {
		t.Fatalf("hook ran %d times before return, want 1", ran)
	}
	if err == nil || !strings.Contains(err.Error(), "hook seed") {
		t.Fatalf("err = %v, want the failing hook named", err)
	}
}
