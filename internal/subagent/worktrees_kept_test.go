package subagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"dyna-agent/internal/worktrees"
)

// A clean tree kept only because its owner is still open needs no notice.
// Trees a person may need to act on still appear.
func TestCleanupNoticeSkipsCleanTreesWithOpenOwner(t *testing.T) {
	got := cleanupNotice(nil, []worktrees.Tree{
		{Path: "/w/open", Branch: "open", Action: "kept", Reason: "owning session is open", OwnerOpen: true},
		{Path: "/w/main", Branch: "main", Action: "kept", Main: true},
		{Path: "/w/gone", Branch: "gone", Action: "removed"},
		{Path: "/w/dirty", Branch: "dirty", Action: "kept", Dirty: true, DirtyFiles: []string{"a.go"}, Reason: "uncommitted changes or untracked files"},
	})
	if len(got) != 1 || got[0].Path != "/w/dirty" || !slices.Equal(got[0].Dirty, []string{"a.go"}) {
		t.Fatalf("kept trees = %+v", got)
	}
}

// A cleanup that ran out of time, or that engine shutdown canceled, posts
// nothing: the scheduled maintenance pass retries the deferred trees and the
// prune. A real safety keep is still reported.
func TestCleanupNoticeSkipsTimeoutAndDeferredTrees(t *testing.T) {
	timeout := fmt.Errorf("worktree cleanup /repo: prune worktree metadata: %w", context.DeadlineExceeded)
	canceled := fmt.Errorf("worktree cleanup /repo: %w", context.Canceled)
	deferred := []worktrees.Tree{{Path: "/w/late", Action: "kept", Deferred: true, Reason: "cleanup ran out of time; the scheduled cleanup retries it"}}
	dirty := []worktrees.Tree{{Path: "/w/dirty", Branch: "dirty", Action: "kept", Dirty: true, DirtyFiles: []string{"a.go"}, Reason: "uncommitted changes or untracked files"}}
	if got := cleanupNotice(timeout, deferred); len(got) != 0 {
		t.Fatalf("timed-out cleanup notice = %+v", got)
	}
	if got := cleanupNotice(canceled, deferred); len(got) != 0 {
		t.Fatalf("canceled cleanup notice = %+v", got)
	}
	if got := cleanupNotice(canceled, dirty); len(got) != 1 || got[0].Path != "/w/dirty" || !slices.Equal(got[0].Dirty, []string{"a.go"}) {
		t.Fatalf("cancel hid a dirty tree: %+v", got)
	}
	if got := cleanupNotice(errors.New("cannot trust worktree ownership"), nil); len(got) != 1 || got[0].Reason == "" {
		t.Fatalf("other cleanup errors must still notify: %+v", got)
	}
}
