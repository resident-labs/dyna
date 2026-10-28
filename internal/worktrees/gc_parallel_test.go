package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

// Removal workers must overlap. The hook holds each worker until four are in
// it at once; a serial cleanup never reaches four and fails at the deadline.
func TestGCRemovesTreesConcurrently(t *testing.T) {
	repo := gcRepo(t)
	const want = 4
	var paths []string
	for i := range want {
		path := gcTree(t, repo, fmt.Sprintf("dyna/rm-%d", i), "closed")
		ageGCTree(t, path)
		paths = append(paths, path)
	}
	var arrived sync.WaitGroup
	arrived.Add(want)
	met := make(chan struct{})
	go func() { arrived.Wait(); close(met) }()
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := GC(context.Background(), repo, GCOptions{
		OwnerClosed: func(string) (bool, error) { return true, nil },
		beforeRemove: func(string) {
			arrived.Done()
			select {
			case <-met:
			case <-deadline.Done():
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if deadline.Err() != nil {
		t.Fatal("removal rechecks never overlapped")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("closed tree %s remains: %v", path, err)
		}
	}
}
