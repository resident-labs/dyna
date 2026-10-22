package computer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func TestPlaywrightInstallLockStopsOnCancellation(t *testing.T) {
	storetest.Isolate(t)
	dir := filepath.Join(store.DataDir(), "browser", "deps")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, ".install.lock")
	unlock, err := store.Lock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(unlock)
	defer release()
	var installs atomic.Int32
	previous := installPlaywright
	installPlaywright = func(ctx context.Context, _ string, _ []string, _ io.Writer) error {
		installs.Add(1)
		return ctx.Err()
	}
	t.Cleanup(func() { installPlaywright = previous })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	contended := make(chan string, 1)
	ctx = store.WithFlockContentionHook(ctx, func(path string) {
		select {
		case contended <- path:
		default:
		}
	})
	done := make(chan error, 1)
	go func() { _, err := playwrightModule(ctx); done <- err }()
	readyBound := time.NewTimer(30 * time.Second)
	defer readyBound.Stop()
	select {
	case path := <-contended:
		if path != lockPath {
			t.Errorf("contended path %q, want %q", path, lockPath)
		}
	case err := <-done:
		t.Fatalf("install returned before lock cancellation: %v", err)
	case <-readyBound.C:
		// Release and join the old blocking implementation before failing the test.
		cancel()
		release()
		<-done
		t.Fatal("browser install did not enter the cancellable lock wait")
	}
	readyBound.Stop()
	cancel()
	cancelBound := time.NewTimer(30 * time.Second)
	defer cancelBound.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled install: %v", err)
		}
	case <-cancelBound.C:
		release()
		<-done
		t.Fatal("cancelled browser install stayed behind the held lock")
	}
	if installs.Load() != 0 {
		t.Error("cancelled lock wait reached the installer")
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); !os.IsNotExist(err) {
		t.Errorf("cancelled lock wait changed install files: %v", err)
	}
	// Cancellation closed only the contender. The original lock is still held.
	if unlock, owned, err := store.TryLock(lockPath); err != nil || owned {
		if owned {
			unlock()
		}
		t.Fatalf("contender released the original lock: owned=%v err=%v", owned, err)
	}
	release()
	if unlock, owned, err := store.TryLock(lockPath); err != nil || !owned {
		t.Fatalf("cancelled contender leaked its lock: owned=%v err=%v", owned, err)
	} else {
		unlock()
	}
}
