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

// A moving default branch refreshes git state without another full file walk.
// The removal recheck must still read the current files.
func TestCleanupReusesUsageAcrossDefaultBranchMoves(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "usage-cache", "closed")
	gitDir := git(t, repo, "rev-parse", "--absolute-git-dir")
	f, err := os.OpenFile(filepath.Join(gitDir, "info", "exclude"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\nignored-output\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(path, "ignored-output")
	if err := os.WriteFile(output, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	head := git(t, path, "rev-parse", "HEAD")
	opts := &GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }}
	now := time.Now()
	one := Tree{Path: path, head: head, sessionID: "closed"}
	inspectCached(t.Context(), repo, "refs/remotes/origin/main", "old-default", opts, &one, now)
	if one.protected != "" || one.Size == 0 {
		t.Fatalf("first inspection: %+v", one)
	}
	if err := os.WriteFile(output, make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	two := Tree{Path: path, head: head, sessionID: "closed"}
	inspectCached(t.Context(), repo, "refs/remotes/origin/main", "new-default", opts, &two, now.Add(time.Second))
	if two.Size != one.Size {
		t.Fatalf("branch move repeated usage walk: %d, want %d", two.Size, one.Size)
	}
	fresh := Tree{Path: path, head: head, sessionID: "closed"}
	inspectCached(t.Context(), repo, "refs/remotes/origin/main", "new-default", &GCOptions{Path: path, OwnerClosed: opts.OwnerClosed}, &fresh, now.Add(time.Second))
	if fresh.Size <= two.Size {
		t.Fatalf("removal recheck missed updated files: %d <= %d", fresh.Size, two.Size)
	}
}
