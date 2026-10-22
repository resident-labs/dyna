package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFlockContextCancellationAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.lock")
	held, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	contender, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := FlockContext(ctx, contender); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock returned %v", err)
	}
	if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("cancelled caller changed the held lock: %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := FlockContext(context.Background(), contender); err != nil {
		t.Fatalf("released lock: %v", err)
	}
}
