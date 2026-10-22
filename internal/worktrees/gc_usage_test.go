package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGCSkipsUsageForOpenOwner(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "active", "open")
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("generated.bin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "generated.bin"), []byte(strings.Repeat("cache", 1024)), 0600); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		trees, err := GC(context.Background(), repo, GCOptions{DryRun: true, OwnerClosed: func(string) (bool, error) { return false, nil }})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tree := range trees {
			if tree.Size != 0 {
				t.Errorf("pass %d measured %d bytes for retained tree %s", pass, tree.Size, tree.Path)
			}
			if tree.Path == path {
				found = true
				if tree.Action != "kept" || !tree.OwnerOpen {
					t.Fatalf("open-owner decision: %+v", tree)
				}
			}
		}
		if !found {
			t.Fatal("active worktree was not reported")
		}
	}
	// Closed-owner candidates still need size for the removal report.
	trees, err := GC(context.Background(), repo, GCOptions{DryRun: true, Path: path, OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].Size == 0 || trees[0].Action != "would remove" {
		t.Fatalf("closed-owner candidate: %+v", trees)
	}
}

func TestGCSkipsUsageForRecentUnknownTree(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "recent-unknown", "")
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("generated/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(path, "generated", "nested")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output"), []byte("generated data"), 0600); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatal(err)
	}
	trees, err := GC(context.Background(), repo, GCOptions{DryRun: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].Action != "kept" || trees[0].Size != 0 || trees[0].Age >= 24*time.Hour {
		t.Fatalf("recent unknown tree was measured: %+v", trees)
	}
	// An old top directory cannot hide recent nested file activity.
	ageGCTree(t, path)
	output := filepath.Join(dir, "output")
	if err := os.Chtimes(output, recent, recent); err != nil {
		t.Fatal(err)
	}
	trees, err = GC(context.Background(), repo, GCOptions{DryRun: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].Action != "kept" || trees[0].Size == 0 || trees[0].Age >= 24*time.Hour {
		t.Fatalf("recent nested activity was not measured: %+v", trees)
	}
	// Only a complete old tree can become a removal candidate.
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(output, old, old); err != nil {
		t.Fatal(err)
	}
	trees, err = GC(context.Background(), repo, GCOptions{DryRun: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 1 || trees[0].Action != "would remove" || trees[0].Size == 0 {
		t.Fatalf("old removal candidate was not measured: %+v", trees)
	}
}
