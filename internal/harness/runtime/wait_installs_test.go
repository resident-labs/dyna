package runtime

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
)

// D-143: EnsureBundle installs in the background so one shared install outlives
// a caller that stops waiting, and that install keeps writing into the bundle
// root. A test that removes its temp tree must wait for it first.
func TestWaitInstallsWaitsForAnAbandonedInstall(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { _ = removeBundle(root) })
	// The virtual clock keeps the install's ten-minute budget out of the test,
	// and synctest.Wait reports when the waiter is blocked, so no real time
	// decides whether WaitInstalls waited.
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		m := &BundleManager{Root: root, Run: func(ctx context.Context, args, env []string) error {
			once.Do(func() { close(started) })
			<-release
			return fixtureInstall(ctx, args, env)
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _, _, _ = (&FirstUse{Bundles: m}).EnsureBundle(ctx, "claude", "1.3.0") }()
		<-started
		// The caller stops waiting; the shared install keeps writing.
		cancel()
		waited := make(chan error, 1)
		go func() { waited <- WaitInstalls(context.Background()) }()
		synctest.Wait()
		select {
		case err := <-waited:
			t.Fatalf("WaitInstalls returned %v while the install was still blocked", err)
		default:
		}
		close(release)
		if err := <-waited; err != nil {
			t.Fatal(err)
		}
		if _, err := m.Resolve("claude", "1.3.0"); err != nil {
			t.Fatalf("the install did not finish before WaitInstalls returned: %v", err)
		}
	})
}
