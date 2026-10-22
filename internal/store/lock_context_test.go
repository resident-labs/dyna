package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLockContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.lock")
	held, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan string, 1)
	ctx = WithFlockContentionHook(ctx, func(path string) {
		select {
		case waiting <- path:
		default:
		}
	})
	free, err := LockContext(ctx, filepath.Join(filepath.Dir(path), "free.lock"))
	if err != nil {
		t.Fatal(err)
	}
	free()
	done := make(chan struct{})
	var lockErr error
	go func() {
		defer close(done)
		unlock, err := LockContext(ctx, path)
		lockErr = err
		if unlock != nil {
			unlock()
		}
	}()
	defer func() { cancel(); held(); <-done }()
	deadline, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stop()
	select {
	case contendedPath := <-waiting:
		if contendedPath != path {
			t.Fatalf("contention hook reported %q, want %q", contendedPath, path)
		}
	case <-done:
		t.Fatalf("caller escaped the held lock: %v", lockErr)
	case <-deadline.Done():
		t.Fatal("caller did not report lock contention")
	}
	cancel()
	select {
	case <-done:
		if !errors.Is(lockErr, context.Canceled) {
			t.Fatalf("waiting lock = %v", lockErr)
		}
	case <-deadline.Done():
		t.Fatal("canceled caller remained blocked behind the held lock")
	}
}

func TestLockContextReleasesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "released.lock")
	unlock, err := LockContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if again, owned, err := TryLock(path); err != nil || owned {
		if again != nil {
			again()
		}
		unlock()
		t.Fatalf("held lock = %v, %v", owned, err)
	}
	unlock()
	again, owned, err := TryLock(path)
	if err != nil || !owned {
		t.Fatalf("released lock = %v, %v", owned, err)
	}
	again()
}
