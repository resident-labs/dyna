package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The scan inspects trees concurrently. Each result must still describe its own tree.
func TestScanInspectsTreesConcurrently(t *testing.T) {
	repo := gcRepo(t)
	var paths []string
	for i := range 12 {
		path := gcTree(t, repo, fmt.Sprintf("dyna/par-%d", i), "open")
		if i%3 == 0 {
			if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, path)
	}
	trees, err := GC(context.Background(), repo, GCOptions{DryRun: true, OwnerClosed: func(string) (bool, error) { return false, nil }})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Tree{}
	for _, tr := range trees {
		byPath[tr.Path] = tr
	}
	for i, path := range paths {
		tr, ok := byPath[path]
		if !ok || tr.Dirty != (i%3 == 0) {
			t.Fatalf("tree %d: dirty=%v found=%v", i, tr.Dirty, ok)
		}
	}
}
