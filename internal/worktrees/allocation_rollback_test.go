package worktrees

import (
	"context"
	"testing"
)

func TestNewForAllocationFailureRemovesNewBranch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := initRepo(t)
	old := portFree
	portFree = func(int) bool { return false }
	t.Cleanup(func() { portFree = old })
	if _, err := NewFor(context.Background(), repo, "failed-allocation", "HEAD", "", ""); err == nil {
		t.Fatal("expected allocation failure")
	}
	if _, err := gitOutput(context.Background(), repo, "show-ref", "--verify", "refs/heads/failed-allocation"); err == nil {
		t.Fatal("failed allocation left its branch behind")
	}
}
