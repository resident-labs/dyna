package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktrees/worktreetest"
)

// A close that asked to keep the worktrees locks them only after every check
// that can refuse it and after its turn stops, so a refused close changes no
// lock and a close that goes keeps the tree cleanup would remove.
func TestCloseKeepsWorktreesOnlyAfterTheCloseChecks(t *testing.T) {
	storetest.Isolate(t)
	m, rooms, _, metas := actorFixture(t)
	m.Rooms = rooms
	t.Cleanup(authority.InEngine())
	tree := worktreetest.Tracked(t)
	if err := worktrees.Track(worktrees.Record{Path: tree, Session: metas["other"].ID}); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})

	// A source that is consolidating may not close, so its tree stays unlocked.
	if _, err := m.Store.UpdateMeta(metas["other"].ID, func(meta *Meta) { meta.Consolidating = "c-1" }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseWithOptions(owner, metas["other"].ID, CloseOptions{KeepWorktrees: true}); err == nil {
		t.Fatal("a consolidating source closed")
	}
	if worktreeLocked(t, tree) {
		t.Fatal("a refused close locked the session's worktree")
	}

	// The same close once it can run keeps the tree for the handoff.
	if _, err := m.Store.UpdateMeta(metas["other"].ID, func(meta *Meta) { meta.Consolidating = "" }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseWithOptions(owner, metas["other"].ID, CloseOptions{KeepWorktrees: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if !worktreeLocked(t, tree) {
		t.Fatal("a close that asked to keep the tree left it unlocked")
	}
}

// worktreeLocked reports whether git holds a lock on the worktree, the same
// Asking to keep on a session that is already closed still keeps the trees:
// they can outlive the close, and cleanup may not have reached them yet.
func TestCloseKeepsWorktreesOnAClosedSession(t *testing.T) {
	storetest.Isolate(t)
	m, rooms, _, metas := actorFixture(t)
	m.Rooms = rooms
	t.Cleanup(authority.InEngine())
	tree := worktreetest.Tracked(t)
	if err := worktrees.Track(worktrees.Record{Path: tree, Session: metas["other"].ID}); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
	if _, err := m.CloseWithOptions(owner, metas["other"].ID, CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if worktreeLocked(t, tree) {
		t.Fatal("a close that did not ask to keep the tree locked it")
	}
	// The same call on the closed session honors the flag instead of returning
	// before it keeps anything.
	if _, err := m.CloseWithOptions(owner, metas["other"].ID, CloseOptions{Force: true, KeepWorktrees: true}); err != nil {
		t.Fatal(err)
	}
	if !worktreeLocked(t, tree) {
		t.Fatal("a keep on an already closed session was a silent no-op")
	}
}

// worktreeLocked reports whether git holds a lock on the worktree, the same
// marker cleanup reads and keeps.
func worktreeLocked(t *testing.T, path string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", path, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatalf("git dir of %s: %v", path, err)
	}
	_, err = os.Stat(filepath.Join(strings.TrimSpace(string(out)), "locked"))
	return err == nil
}
