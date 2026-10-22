package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
)

func TestOrphanedListsOnlyKeptTreesWithClosedSessionOwners(t *testing.T) {
	repo := gcRepo(t)
	storetest.ClearStamp(t)
	writeOwner := func(id, body string) {
		t.Helper()
		recordtest.WriteRaw(t, runstore.SessionsDir(), id, []byte(body))
	}
	writeOwner("closed", `{"closed":"2026-01-01T00:00:00Z"}`)
	writeOwner("open", `{}`)
	closed := gcTree(t, repo, "closed-dirty", "closed")
	open := gcTree(t, repo, "open-dirty", "open")
	unknown := gcTree(t, repo, "unknown-dirty", "missing")
	gcTree(t, repo, "closed-clean", "closed")
	for _, path := range []string{closed, open, unknown} {
		if err := os.WriteFile(filepath.Join(path, "unsaved"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	trees, err := Orphaned(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].Path != closed || trees[0].Ahead != 0 || len(trees[0].DirtyFiles) != 1 {
		t.Fatalf("orphans: %+v", trees)
	}
	for _, path := range []string{closed, open, unknown} {
		if _, err := os.Stat(filepath.Join(path, "unsaved")); err != nil {
			t.Fatal(err)
		}
	}
	// An unmerged commit remains visible after its dirty file is committed.
	git(t, closed, "add", ".")
	git(t, closed, "commit", "-m", "unlanded")
	trees, err = Orphaned(context.Background(), repo)
	if err != nil || len(trees) != 1 || trees[0].Ahead != 1 || trees[0].Dirty {
		t.Fatalf("unlanded orphans: %+v, %v", trees, err)
	}
}
