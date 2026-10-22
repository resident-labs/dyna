package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/lockrank"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func TestMetadataLocksCancelAndRelease(t *testing.T) {
	storetest.Isolate(t)
	s := &Store{Root: t.TempDir()}
	id := "ses_20261020-000000-1234abcd"
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		with       func(context.Context, func() error) error
	}{
		{"session", filepath.Join(dir, "session.lock"), func(ctx context.Context, fn func() error) error { return s.withLockContext(ctx, id, fn) }},
		{"names", filepath.Join(s.Root, ".names.lock"), func(ctx context.Context, fn func() error) error {
			unlock, err := lockNamesContext(ctx, s)
			if err != nil {
				return err
			}
			defer unlock()
			return fn()
		}},
		{"gate", filepath.Join(dir, "gate.lock"), func(ctx context.Context, fn func() error) error {
			return s.withGateLockContext(ctx, id, func(string) error { return fn() })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := store.Lock(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			called := false
			err = tc.with(ctx, func() error { called = true; return nil })
			held()
			if !errors.Is(err, context.Canceled) || called {
				t.Fatalf("cancelled lock ran callback: %v, %v", called, err)
			}
			// A failed session or gate wait must release its rank too.
			lockrank.Acquire(lockrank.Names)()
			want := errors.New("callback failed")
			err = tc.with(t.Context(), func() error {
				unlock, owned, err := store.TryLock(tc.path)
				if owned {
					unlock()
				}
				if err != nil || owned {
					t.Fatalf("callback does not hold the lock: %v, %v", owned, err)
				}
				return want
			})
			if !errors.Is(err, want) {
				t.Fatalf("callback error changed: %v", err)
			}
			unlock, owned, err := store.TryLock(tc.path)
			if owned {
				unlock()
			}
			if err != nil || !owned {
				t.Fatalf("callback left the lock held: %v, %v", owned, err)
			}
			lockrank.Acquire(lockrank.Names)()
		})
	}
}
