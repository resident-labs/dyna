package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// The registry commit for removed trees must not run inside the removal lock.
// One slow commit would serialize every remaining removal, which is what
// blows the cleanup deadline on large registries. The hook blocks the batch
// commit; both trees must already be gone, and their records disappear only
// after the commit is released.
func TestGCCommitsRemovalsOutsideTheRemovalLock(t *testing.T) {
	repo := gcRepo(t)
	var paths []string
	for i := range 2 {
		path := gcTree(t, repo, fmt.Sprintf("dyna/batch-%d", i), "closed")
		ageGCTree(t, path)
		paths = append(paths, path)
	}
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	realBatch := markRemovedBatch
	markRemovedBatch = func(ctx context.Context, removed []Record) error {
		arrived <- struct{}{}
		<-release
		return realBatch(ctx, removed)
	}
	t.Cleanup(func() { markRemovedBatch = realBatch })
	done := make(chan error, 1)
	go func() {
		_, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
		done <- err
	}()
	<-arrived
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("tree %s waits for the registry commit: %v", path, err)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := Get(path); err == nil {
			t.Fatalf("record of %s survived the batch commit", path)
		}
	}
}

// The registry commit of one cleanup pass runs on its own context: a cleanup
// whose deadline expires keeps the progress its workers already made, and the
// next scheduled cleanup retries the trees that stayed.
func TestGCBatchCommitSurvivesExpiredDeadline(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "dyna/deadline", "closed")
	ageGCTree(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	realBatch := markRemovedBatch
	var batchErr error
	markRemovedBatch = func(bctx context.Context, removed []Record) error {
		// The cleanup deadline expires while the commit runs.
		cancel()
		if err := bctx.Err(); err != nil {
			t.Errorf("batch commit context died with the cleanup deadline: %v", err)
		}
		batchErr = realBatch(bctx, removed)
		return batchErr
	}
	t.Cleanup(func() { markRemovedBatch = realBatch })
	if _, err := GC(ctx, repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }}); err != nil {
		t.Fatal(err)
	}
	if batchErr != nil {
		t.Fatal(batchErr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("tree remains: %v", err)
	}
	if _, err := Get(path); err == nil {
		t.Fatal("record of the removed tree was not committed")
	}
}

// A registry read failure must keep the tree: removing it anyway would strand
// its record and port forever, because later scans no longer see the tree.
func TestGCKeepsTreeWhenRegistryReadFails(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "dyna/read-fail", "closed")
	ageGCTree(t, path)
	realRead := trackedRecord
	trackedRecord = func(string) (Record, bool, error) {
		return Record{}, false, fmt.Errorf("registry unreadable")
	}
	t.Cleanup(func() { trackedRecord = realRead })
	trees, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tree removed despite a registry read failure: %v; decisions: %#v", err, trees)
	}
	kept := false
	for _, tree := range trees {
		if tree.Path == path {
			kept = tree.Action == "kept" && strings.Contains(tree.Reason, "cannot read worktree registry")
		}
	}
	if !kept {
		t.Fatalf("decision for the unreadable tree = %#v", trees)
	}
}

// A path that another process recreates and tracks between the removal and
// the batch commit keeps its new record: the batch deletes only the record
// version each removal observed, never the path.
func TestGCBatchKeepsARecreatedRecord(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "dyna/race", "closed")
	ageGCTree(t, path)
	realBatch := markRemovedBatch
	markRemovedBatch = func(ctx context.Context, removed []Record) error {
		git(t, repo, "worktree", "add", "-b", "dyna/race-2", path, "origin/main")
		if err := Track(Record{Path: path, Session: "recreated-owner", Branch: "dyna/race-2", Repo: repo, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		return realBatch(ctx, removed)
	}
	t.Cleanup(func() { markRemovedBatch = realBatch })
	if _, err := GC(context.Background(), repo, GCOptions{OwnerClosed: func(string) (bool, error) { return true, nil }}); err != nil {
		t.Fatal(err)
	}
	rec, err := Get(path)
	if err != nil {
		t.Fatalf("recreated record was deleted by the batch: %v", err)
	}
	if rec.Session != "recreated-owner" || rec.Port == 0 {
		t.Fatalf("recreated record = %+v; want the new owner and its port kept", rec)
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

// A commit made while a worker waits for the removal lock must keep the tree.
func TestGCRechecksHeadAfterQueueing(t *testing.T) {
	repo := gcRepo(t)
	path := gcTree(t, repo, "dyna/queued-head", "owner")
	ageGCTree(t, path)
	trees, err := GC(context.Background(), repo, GCOptions{
		OwnerClosed: func(string) (bool, error) { return true, nil },
		beforeLock: func(p string) {
			git(t, p, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "late work")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tree that gained a commit was removed: %v; decisions: %#v", err, trees)
	}
}
