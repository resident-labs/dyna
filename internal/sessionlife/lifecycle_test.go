package sessionlife

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
)

func TestCheckRequiresMatchingOpenSessionEvidence(t *testing.T) {
	root := t.TempDir()
	id := "ses_20261017-120000-abcdef01"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		allowed    bool
	}{
		{"open", `{"id":"` + id + `"}`, true},
		{"closed", `{"id":"` + id + `","closed":"2026-10-17T12:00:00Z"}`, false},
		{"dormant", `{"id":"` + id + `","finished":"2026-10-17T12:00:00Z","status":"dormant"}`, true},
		{"closing", `{"id":"` + id + `","closing":{"token":"operation-a","pid":1}}`, false},
		{"missing identity", `{}`, false},
		{"null evidence", `null`, false},
		{"other identity", `{"id":"ses_20261017-120000-fedcba02"}`, false},
		{"invalid JSON", `{"id":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeRawRecord(t, root, id, []byte(tc.body))
			err := Check(root, id)
			if (err == nil) != tc.allowed {
				t.Fatalf("Check allowed=%t, want %t, error=%v", err == nil, tc.allowed, err)
			}
		})
	}
	if err := Delete(root, id); err != nil {
		t.Fatal(err)
	}
	if err := Check(root, id); err == nil {
		t.Fatal("missing evidence permitted publication")
	}
	for _, external := range []string{"", "codex:external-thread", "claude:external-session"} {
		if err := Check(filepath.Join(root, "missing-root"), external); err != nil {
			t.Fatalf("external owner %q: %v", external, err)
		}
	}
}

func TestPublicationLockSerializesAndCancelsBeforeCallback(t *testing.T) {
	root := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(root, "publish.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// The held lock keeps the waiter in WithPublication's retry loop on the
	// virtual clock, so synctest.Wait can report that state without a timer.
	synctest.Test(t, func(t *testing.T) {
		var called atomic.Bool
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- WithPublication(ctx, root, "publish.lock", func() error { called.Store(true); return nil })
		}()
		synctest.Wait()
		if called.Load() {
			t.Fatal("publication entered while lock held")
		}
		cancel()
		if err := <-done; err == nil {
			t.Fatal("cancelled waiter succeeded")
		}
		if called.Load() {
			t.Fatal("cancelled publication mutated the protected store")
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		if err := WithPublication(context.Background(), root, "publish.lock", func() error { called.Store(true); return nil }); err != nil || !called.Load() {
			t.Fatalf("publication did not recover after release: %v", err)
		}
	})
}

func TestActivatePreservesDormantOrigin(t *testing.T) {
	for _, status := range []string{"dormant", "idle"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			id := "ses_20261017-120000-abcdef01"
			dir := filepath.Join(root, id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := Save(root, id, map[string]string{"id": id, "status": status}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := Activate(root, id); err != nil {
					t.Fatal(err)
				}
			}
			var fields struct {
				Status      string
				DormantWake bool
			}
			if err := Load(root, id, &fields); err != nil {
				t.Fatal(err)
			}
			if fields.Status != "idle" || fields.DormantWake != (status == "dormant") {
				t.Fatalf("activated=%+v from %s", fields, status)
			}
		})
	}
}
