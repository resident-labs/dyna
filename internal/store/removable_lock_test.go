package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRemovableLockRejectsOpenOfRemovedInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication.lock")
	first, owned, err := TryLockAndRemove(path)
	if err != nil || !owned {
		t.Fatalf("first lock: owned=%v, err=%v", owned, err)
	}
	stale, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		first()
		t.Fatal(err)
	}
	first()
	current, owned, err := TryLockAndRemove(path)
	if err != nil || !owned {
		stale.Close()
		t.Fatalf("replacement lock: owned=%v, err=%v", owned, err)
	}
	defer current()
	// The contender opened before removal, but acquires after replacement.
	unlock, owned, err := flockOpened(context.Background(), path, stale, syscall.LOCK_EX|syscall.LOCK_NB, true)
	if owned {
		unlock()
	}
	if err != nil || owned {
		t.Fatalf("stale open entered concurrently: owned=%v, err=%v", owned, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale open removed the replacement: %v", err)
	}
}

func TestRemovableLockRejectsBlockedOpenOfRemovedInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		release()
		t.Fatal(err)
	}
	if err := syscall.Flock(int(stale.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		release()
		stale.Close()
		t.Fatalf("contender was not blocked: %v", err)
	}
	type result struct {
		unlock func()
		owned  bool
		err    error
	}
	entered, done := make(chan struct{}), make(chan result, 1)
	go func() {
		close(entered)
		unlock, owned, err := flockOpened(context.Background(), path, stale, syscall.LOCK_EX, true)
		done <- result{unlock, owned, err}
	}()
	<-entered
	// Cleanup holds the old file while replacing its name. The contender keeps
	// its original open and cannot finish LOCK_EX until release runs below.
	if err := os.Remove(path); err != nil {
		release()
		t.Fatal(err)
	}
	current, owned, err := TryLockAndRemove(path)
	if err != nil || !owned {
		release()
		t.Fatalf("new path: owned=%v, err=%v", owned, err)
	}
	defer current()
	release()
	got := <-done
	if got.owned {
		got.unlock()
	}
	if got.err != nil || got.owned {
		t.Fatalf("blocked contender entered a removed inode: owned=%v, err=%v", got.owned, got.err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("blocked contender removed the current gate: %v", err)
	}
}
