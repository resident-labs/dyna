//go:build linux

package computers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/settings"
)

// browserCleanupComputer registers one computer with a shared save-back lease,
// a master profile and a session profile that already changed login state.
func browserCleanupComputer(t *testing.T, ctx context.Context, id string) *supervisor {
	t.Helper()
	s, err := newComputer(ctx, id, "test", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.cleanup(false) })
	browserTestFile(t, browserMaster(), "Default/Preferences", "master preferences")
	browserTestFile(t, browserMaster(), "Default/Local Storage/state", "signed-in")
	if err := prepareBrowserProfile(ctx, id, settings.Defaults()); err != nil {
		t.Fatal(err)
	}
	browserTestFile(t, computerBrowserProfile(id), "Default/Local Storage/session", "new login")
	browserTestFile(t, computerBrowserProfile(id), "Default/Web Data", "session form data")
	return s
}

// browserCleanupHeld reports whether another open file description holds path.
func browserCleanupHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	held := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil
	f.Close()
	return held
}

// waitBrowserCleanupHeld waits until the daemon holds the cleanup lock, so the
// test acts only after its save-back started.
func waitBrowserCleanupHeld(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if browserCleanupHeld(path) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon never reached its profile release")
		}
	}
}

func assertBrowserCleanupSaved(t *testing.T) {
	t.Helper()
	if got := browserTestRead(t, browserMaster(), "Default/Local Storage/state"); got != "signed-in" {
		t.Fatalf("master lost its saved storage state: %q", got)
	}
	if got := browserTestRead(t, browserMaster(), "Default/Local Storage/session"); got != "new login" {
		t.Fatalf("master missed the saved login: %q", got)
	}
	if got := browserTestRead(t, browserMaster(), "Default/Web Data"); got != "session form data" {
		t.Fatalf("master lost its saved web data: %q", got)
	}
	if got := browserTestRead(t, browserMaster(), "Default/Preferences"); got != "master preferences" {
		t.Fatalf("master preferences changed: %q", got)
	}
}

// The offline path must not release a profile while the daemon still does. It
// waits for the lock, then finds no lease and keeps the saved state.
func TestOfflineDestroyWaitsForDaemonProfileRelease(t *testing.T) {
	browserTestEnvironment(t)
	ctx := context.Background()
	id := testSessionID(t, "offline-waits")
	browserCleanupComputer(t, ctx, id)
	held, err := browserFileLock(ctx, filepath.Join(Dir(id), "cleanup.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	offline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = destroyOffline(offline, id)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("offline destroy ran while the daemon held the profile: %v", err)
	}
	if got := browserTestRead(t, browserMaster(), "Default/Local Storage/state"); got != "signed-in" {
		t.Fatalf("blocked destroy changed the master: %q", got)
	}
	if _, err := os.Stat(filepath.Join(browserMaster(), "Default/Web Data")); !os.IsNotExist(err) {
		t.Fatalf("blocked destroy merged a source it does not own: %v", err)
	}
	held.Close()
	if err := destroyOffline(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertBrowserCleanupSaved(t)
}

// A destroy call that expires hands the work to the offline path while the
// daemon is still saving. Only one of them may release the lease, so the
// master keeps the state the daemon saved instead of merging a consumed source.
func TestDestroyFallbackKeepsSavedBrowserState(t *testing.T) {
	browserTestEnvironment(t)
	ctx := context.Background()
	id := testSessionID(t, "cleanup-race")
	s := browserCleanupComputer(t, ctx, id)
	// Hold the master lock, so the daemon's save-back stays in flight.
	held, err := browserFileLock(ctx, filepath.Join(filepath.Dir(browserMaster()), "profile.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	// Each path reports its attempt, so the test knows the fallback is waiting
	// for the lock the daemon holds instead of guessing from a goroutine start.
	attempts := make(chan struct{}, 8)
	realWait := browserCleanupWait
	browserCleanupWait = func(string) { attempts <- struct{}{} }
	t.Cleanup(func() { browserCleanupWait = realWait })
	daemonDone := make(chan error, 1)
	go func() {
		d := &daemon{ctx: ctx, computers: map[string]*supervisor{id: s}}
		_, err := d.dispatch(ctx, Request{Op: "destroy", Session: id})
		daemonDone <- err
	}()
	waitBrowserCleanupHeld(t, filepath.Join(Dir(id), "cleanup.lock"))
	<-attempts // the daemon's save-back, which now waits for the master lock
	fallbackDone := make(chan error, 1)
	go func() {
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fallbackDone <- destroyOffline(fctx, id)
	}()
	select {
	case <-attempts: // the fallback now waits for the lock the daemon holds
	case <-time.After(5 * time.Second):
		t.Fatal("the fallback never reached the cleanup lock")
	}
	held.Close()
	for _, done := range []chan error{daemonDone, fallbackDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup did not finish")
		}
	}
	assertBrowserCleanupSaved(t)
}
