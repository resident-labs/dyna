package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A cleanup scan reuses a tree's inspection, so a minute-by-minute pass does
// not run git status on every tree again. A single-tree recheck, which guards
// every removal, never reuses it.
func TestCleanupScanReusesInspectionButRecheckReadsFresh(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "cached", "open")
	open := func(string) (bool, error) { return false, nil }
	find := func(trees []Tree) Tree {
		for _, tree := range trees {
			if tree.Path == path {
				return tree
			}
		}
		t.Fatalf("tree %s was not reported", path)
		return Tree{}
	}
	trees, err := GC(context.Background(), repo, GCOptions{DryRun: true, OwnerClosed: open})
	if err != nil {
		t.Fatal(err)
	}
	if find(trees).Dirty {
		t.Fatal("new tree is dirty")
	}
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	trees, err = GC(context.Background(), repo, GCOptions{DryRun: true, OwnerClosed: open})
	if err != nil {
		t.Fatal(err)
	}
	if find(trees).Dirty {
		t.Fatal("cleanup scan inspected the tree again inside the reuse window")
	}
	// The next hourly maintenance pass still reuses the inspection.
	inspections.Lock()
	c := inspections.byPath[path]
	c.at = c.at.Add(-70 * time.Minute)
	inspections.byPath[path] = c
	inspections.Unlock()
	trees, err = GC(context.Background(), repo, GCOptions{DryRun: true, OwnerClosed: open})
	if err != nil {
		t.Fatal(err)
	}
	if find(trees).Dirty {
		t.Fatal("hourly cleanup scan inspected the tree again")
	}
	trees, err = GC(context.Background(), repo, GCOptions{DryRun: true, Path: path, OwnerClosed: open})
	if err != nil {
		t.Fatal(err)
	}
	if !find(trees).Dirty {
		t.Fatal("single-tree recheck reused a stale inspection")
	}
}
