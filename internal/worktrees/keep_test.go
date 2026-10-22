package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
)

// A tree the registry names is skipped only when it is proven absent.
func TestKeepSessionSkipsOnlyAProvenAbsentTree(t *testing.T) {
	repo := gcRepo(t)
	root, err := ManagedRoot()
	if err != nil {
		t.Fatal(err)
	}
	kept := gcTree(t, repo, "keep-lookups", "ses_lookups")
	// A record for a tree that is gone: nothing to keep.
	if err := Track(Record{Path: filepath.Join(root, "gone", "tree"), Session: "ses_lookups"}); err != nil {
		t.Fatal(err)
	}
	paths, err := KeepSession(context.Background(), "ses_lookups", "kept for a manual handoff")
	if err != nil || len(paths) != 1 || paths[0] != kept {
		t.Fatalf("kept = %v, %v; want only the present tree %s", paths, err, kept)
	}
}

// An entry that cannot be inspected is not a proven absence: the keep names it
// and stops the close. The record is made while holder is a directory and
// holder becomes a file afterwards, so Lstat of holder/tree fails with ENOTDIR,
// which os.IsNotExist does not accept. This holds even when the test runs as
// root, because no permission bit is involved.
func TestKeepSessionFailsOnAnEntryItCannotInspect(t *testing.T) {
	_ = gcRepo(t)
	root, err := ManagedRoot()
	if err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(root, "holder")
	if err := os.MkdirAll(holder, 0o700); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(holder, "tree")
	if err := Track(Record{Path: tree, Session: "ses_inspect"}); err != nil {
		t.Fatalf("track the tree while its holder is a directory: %v", err)
	}
	// A regular file where the holder directory was: holder/tree is not a
	// directory entry that Lstat can inspect.
	if err := os.RemoveAll(holder); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(holder, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = KeepSession(context.Background(), "ses_inspect", "kept for a manual handoff")
	if err == nil || !strings.Contains(err.Error(), tree) {
		t.Fatalf("keep error = %v; want an inspect failure naming %s", err, tree)
	}
}

// A recorded directory that git cannot lock is there, not absent: the keep names
// it and stops the close.
func TestKeepSessionFailsOnAnEntryItCannotLock(t *testing.T) {
	_ = gcRepo(t)
	root, err := ManagedRoot()
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Track(Record{Path: plain, Session: "ses_lock"}); err != nil {
		t.Fatal(err)
	}
	_, err = KeepSession(context.Background(), "ses_lock", "kept for a manual handoff")
	if err == nil || !strings.Contains(err.Error(), plain) {
		t.Fatalf("keep error = %v; want a lock failure naming %s", err, plain)
	}
}

// Cleanup also selects the linked tree that holds the session's directory, even
// when the registry still names the earlier owner. The keep locks that tree, so
// cleanup keeps it once both owners are closed.
func TestKeepSessionHoldsTheTreeHoldingTheSessionDirectory(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "inherited", "ses_predecessor")
	recordtest.Write(t, runstore.SessionsDir(), "ses_successor", struct{ ID, Dir string }{ID: "ses_successor", Dir: path})
	kept, err := KeepSession(context.Background(), "ses_successor", "kept for a manual handoff")
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0] != path {
		t.Fatalf("kept = %v; want the inherited tree %s", kept, path)
	}
	trees, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]Tree{}
	for _, tree := range trees {
		actions[tree.Path] = tree
	}
	if got := actions[path]; got.Action != "kept" || !strings.HasPrefix(got.Reason, "worktree is locked") {
		t.Fatalf("inherited tree = %#v; want kept because it is locked", got)
	}
}

// A manual handoff keeps the source trees: KeepSession locks every worktree the
// session's record names, so cleanup keeps it and removes a tree that no handoff
// asked for.
func TestKeepSessionHoldsTheSourceWorktrees(t *testing.T) {
	repo := gcRepo(t)
	source := gcTree(t, repo, "handoff-source", "ses_source")
	other := gcTree(t, repo, "handoff-other", "ses_other")
	kept, err := KeepSession(context.Background(), "ses_source", "kept for a manual handoff")
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0] != source {
		t.Fatalf("kept = %v; want only the source tree %s", kept, source)
	}
	// One cleanup pass, as maintenance runs it: the closed source's locked tree
	// stays and the other session's clean merged tree goes.
	trees, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]Tree{}
	for _, tree := range trees {
		actions[tree.Path] = tree
	}
	if got := actions[source]; got.Action != "kept" || !strings.HasPrefix(got.Reason, "worktree is locked") {
		t.Fatalf("kept source tree = %#v; want kept because it is locked", got)
	}
	if got := actions[other]; got.Action != "removed" {
		t.Fatalf("other session's tree = %#v; want removed", got)
	}
	// Keeping it again is not an error, so a repeated handoff close is safe.
	if _, err := KeepSession(context.Background(), "ses_source", "kept for a manual handoff"); err != nil {
		t.Fatalf("second keep of a locked tree: %v", err)
	}
}
