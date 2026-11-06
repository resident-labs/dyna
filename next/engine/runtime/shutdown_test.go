package runtime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

// Exercise the registrations in Run itself, not a test-only copy of the stack.
// Missing profiles stop startup immediately after the engine start is recorded.
// The cleanup context is already canceled by shutdown.run, so the stop write
// must use its own context and complete before the store's cleanup.
func TestRunRecordsEngineStopBeforeStoreCloses(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	data := t.TempDir()
	before := time.Now().UnixMilli()
	err := Run(context.Background(), Config{Data: data, Profiles: filepath.Join(data, "missing-profiles.json"), Version: "test-build"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run should stop at missing profiles, got %v", err)
	}
	after := time.Now().UnixMilli()
	st, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var build string
	var started int64
	var stopped sql.NullInt64
	if err := st.Read().QueryRow("SELECT json_extract(value,'$.build'),json_extract(value,'$.started_at'),json_extract(value,'$.stopped_at') FROM meta WHERE key='engine_state'").Scan(&build, &started, &stopped); err != nil {
		t.Fatal(err)
	}
	if !stopped.Valid {
		t.Fatal("engine stop was not recorded before the store closed")
	}
	if build != "test-build" || started < before || stopped.Int64 < started || stopped.Int64 > after {
		t.Fatalf("engine state build=%q started=%d stopped=%v, run interval %d..%d", build, started, stopped, before, after)
	}
}

func TestEngineShutdown(t *testing.T) {
	// This exercises Run's entire shutdown stack, not an independent wait
	// helper. synctest supplies a virtual clock, with no wall-clock sleeps.
	for _, stuck := range []bool{false, true} {
		name := "cooperative"
		if stuck {
			name = "stuck workers"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				shutdown := engineShutdown{stop: cancel}
				began := time.Now()
				release := make(chan struct{})
				defer close(release)
				completed := make(chan struct{})
				close(completed)
				// Register the same worker joins Run wires, in acquisition order.
				for _, worker := range []string{"host recovery", "reconciler", "subscriptions", "image builds", "archive expiry", "CI pool"} {
					done := (<-chan struct{})(completed)
					if stuck && (worker == "CI pool" || worker == "reconciler") {
						done = release
					}
					shutdown.join(worker, done)
				}
				poller := make(chan struct{})
				time.AfterFunc(3*time.Second, func() { close(poller) })
				shutdown.join("PR poller", poller)

				// Register final cleanup in acquisition order, just as Run does.
				// Each fake needs time to *complete*, after the worker budget ran
				// out. Merely starting these callbacks must not pass this test.
				var cleaned []string
				for _, resource := range []string{"store", "proxy", "sockets"} {
					shutdown.cleanup(resource, func() {
						<-time.After(500 * time.Millisecond)
						cleaned = append(cleaned, resource)
					})
				}
				shutdown.run()
				if ctx.Err() != context.Canceled {
					t.Fatal("early-error return did not cancel workers")
				}
				if want := []string{"sockets", "proxy", "store"}; !reflect.DeepEqual(cleaned, want) {
					t.Fatalf("cleanup completed %v, want %v in order", cleaned, want)
				}
				want := 4500 * time.Millisecond
				if stuck {
					want = shutdownTimeout + 1500*time.Millisecond
				}
				if elapsed := time.Since(began); elapsed != want {
					t.Fatalf("shutdown took %s, want %s total", elapsed, want)
				}
				t.Logf("post-drain shutdown including completed final cleanup: %s (virtual clock)", want)
			})
		})
	}
	t.Run("stuck final cleanup is bounded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			began := time.Now()
			release := make(chan struct{})
			defer close(release)
			shutdown := engineShutdown{stop: func() {}}
			shutdown.join("stuck CI", release)
			shutdown.cleanup("stuck sockets", func() { <-release })
			shutdown.run()
			if elapsed := time.Since(began); elapsed != shutdownTimeout+cleanupTimeout {
				t.Fatalf("stuck final cleanup took %s, want 18s maximum", elapsed)
			}
		})
	})
}
