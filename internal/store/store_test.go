package store_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

func TestLockInterlocksWithOldFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "room.lock")
	// This is the old room lock path: open a separate descriptor, then flock it.
	old, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := syscall.Flock(int(old.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(old.Fd()), syscall.LOCK_UN)
	started := make(chan struct{})
	acquired := make(chan func(), 1)
	failed := make(chan error, 1)
	go func() {
		close(started)
		unlock, err := store.Lock(path)
		if err != nil {
			failed <- err
			return
		}
		acquired <- unlock
	}()
	<-started
	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("new lock passed the held old lock")
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := syscall.Flock(int(old.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case unlock := <-acquired:
		defer unlock()
		// Reverse direction: the old path cannot acquire the new lock.
		err := syscall.Flock(int(old.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			t.Fatalf("old lock did not contend with new lock: %v", err)
		}
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("new lock did not acquire after old unlock")
	}
}

func TestTryLockTakesAndYields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workers.lock")
	unlock, owned, err := store.TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("TryLock did not take the free lock")
	}
	if _, owned, err := store.TryLock(path); err != nil || owned {
		t.Fatalf("TryLock on the held lock = %v, %v; want false, nil", owned, err)
	}
	unlock()
	unlock, owned, err = store.TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("TryLock did not take the released lock")
	}
	unlock()
}

func TestWriteJSONGoldenAndMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want string
		mode os.FileMode
	}{
		{"fields", struct {
			Z string `json:"z"`
			A []int  `json:"a"`
		}{"<tag>", []int{2, 1}}, "{\n  \"z\": \"\\u003ctag\\u003e\",\n  \"a\": [\n    2,\n    1\n  ]\n}\n", 0o600},
		{"map", map[string]int{"z": 2, "a": 1}, "{\n  \"a\": 1,\n  \"z\": 2\n}\n", 0o644},
		{"raw", json.RawMessage(`{"z":2,"a":1}`), `{"z":2,"a":1}`, 0o600},
		{"raw-nil", json.RawMessage(nil), "null", 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "doc.json")
			if err := store.WriteJSON(path, tc.v, tc.mode); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("bytes = %q, want %q; error %v", got, tc.want, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.mode {
				t.Fatalf("mode = %o", info.Mode().Perm())
			}
		})
	}
}

func TestReadAndUpdate(t *testing.T) {
	dir := t.TempDir()
	path, lock := filepath.Join(dir, "doc.json"), filepath.Join(dir, "doc.lock")
	var value struct {
		Count int `json:"count"`
	}
	if err := store.ReadJSON(path, &value); err != nil || value.Count != 0 {
		t.Fatalf("missing: %+v %v", value, err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			var v struct {
				Count int `json:"count"`
			}
			if err := store.Update(lock, path, &v, func() error { v.Count++; return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := store.ReadJSON(path, &value); err != nil || value.Count != 12 {
		t.Fatalf("update: %+v %v", value, err)
	}
	before, _ := os.ReadFile(path)
	sentinel := errors.New("stop")
	if err := store.Update(lock, path, &value, func() error { value.Count++; return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed callback changed file")
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := store.Update(lock, path, &value, func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("invalid JSON: %v, called %v", err, called)
	}
	// Read failure must release the lock.
	unlock, err := store.Lock(lock)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestPathsMatchExistingResolvers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, custom := range []bool{false, true} {
		if custom {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
		} else {
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("XDG_DATA_HOME", "")
		}
		if got, want := store.ConfigDir(), filepath.Dir(profile.DefaultPath()); got != want {
			t.Fatalf("config = %q, want %q", got, want)
		}
		want := filepath.Join(os.Getenv("HOME"), ".local", "share", "dyna")
		if custom {
			want = filepath.Join(os.Getenv("XDG_DATA_HOME"), "dyna")
		}
		if got := store.DataDir(); got != want {
			t.Fatalf("data = %q, want %q", got, want)
		}
	}
}

func TestErrorsLeaveExistingDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, make(chan int), 0o600); err == nil {
		t.Fatal("expected marshal error")
	}
	if err := store.WriteJSON(path, json.RawMessage("{broken"), 0o600); err == nil {
		t.Fatal("expected invalid raw JSON error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Fatal("marshal failure changed file")
	}
	missing := filepath.Join(t.TempDir(), "missing", "doc.json")
	if err := store.WriteJSON(missing, 1, 0o600); !os.IsNotExist(err) {
		t.Fatalf("missing parent: %v", err)
	}
	if _, err := store.Lock(missing); !os.IsNotExist(err) {
		t.Fatalf("missing lock parent: %v", err)
	}
}
