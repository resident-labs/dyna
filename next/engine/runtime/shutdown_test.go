package runtime

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestShutdownSharesPostDrainDeadline(t *testing.T) {
	// synctest injects a virtual clock into timers and context deadlines: this
	// measures the shutdown path without waiting for any wall-clock timeout.
	for _, stuck := range []bool{false, true} {
		name := "cooperative"
		if stuck {
			name = "stuck"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				starts := 0
				budget := shutdownWait{start: func() (context.Context, context.CancelFunc) {
					starts++
					return context.WithTimeout(context.Background(), shutdownTimeout)
				}}
				defer budget.close()
				// The first worker joins after 3 s, even if a later worker is stuck.
				worker := make(chan struct{})
				time.AfterFunc(3*time.Second, func() { close(worker) })
				budget.wait("worker", worker)
				if elapsed := time.Since(began); elapsed != 3*time.Second {
					t.Fatalf("early completion took %s, want 3s", elapsed)
				}

				want := 3 * time.Second
				if stuck {
					// Model drain has already finished. A CI worker ignoring its
					// cancellation must not hold up sockets or proxy cleanup.
					release := make(chan struct{})
					defer close(release)
					budget.run("stuck CI", func() { <-release })
					budget.wait("stuck reconciler", make(chan struct{}))
					want = shutdownTimeout
				}
				proxyStopped := make(chan struct{})
				budget.run("proxy", func() { close(proxyStopped) })
				synctest.Wait()
				select {
				case <-proxyStopped:
				default:
					t.Fatal("stuck subsystem prevented proxy stop")
				}
				if elapsed := time.Since(began); elapsed != want {
					t.Fatalf("shutdown took %s, want %s total (not one timeout per wait)", elapsed, want)
				}
				if starts != 1 {
					t.Fatalf("started %d budgets, want one shared deadline", starts)
				}
				t.Logf("post-drain shutdown: %s (virtual clock)", want)
			})
		})
	}
}
