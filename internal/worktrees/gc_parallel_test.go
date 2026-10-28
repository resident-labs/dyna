package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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

// Removal workers must overlap. The hook holds each worker after its rechecks
// until four are waiting at once. A serial cleanup never gets there, so the
// test blocks instead of passing.
func TestGCRemovesTreesConcurrently(t *testing.T) {
	repo := gcRepo(t)
	const want = 4
	var paths []string
	for i := range want {
		path := gcTree(t, repo, fmt.Sprintf("dyna/rm-%d", i), "closed")
		ageGCTree(t, path)
		paths = append(paths, path)
	}
	arrived := make(chan struct{}, want)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := GC(context.Background(), repo, GCOptions{
			OwnerClosed: func(string) (bool, error) { return true, nil },
			beforeLock: func(string) {
				arrived <- struct{}{}
				<-release
			},
		})
		done <- err
	}()
	for range want {
		<-arrived
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("closed tree %s remains: %v", path, err)
		}
	}
}

// An owner that becomes active while its worker waits for the removal lock
// must keep the tree.
func TestGCRechecksOwnerAfterQueueing(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "dyna/queued", "owner")
	ageGCTree(t, path)
	var active atomic.Bool
	trees, err := GC(context.Background(), repo, GCOptions{
		OwnerClosed: func(string) (bool, error) { return !active.Load(), nil },
		beforeLock:  func(string) { active.Store(true) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tree of a reactivated owner was removed: %v; decisions: %#v", err, trees)
	}
}
