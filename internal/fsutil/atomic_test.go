package fsutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileReplacesContentsAndMode(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode os.FileMode
		sync bool
	}{
		{"private", 0o600, false},
		{"synced", 0o644, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
				t.Fatal(err)
			}
			old, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			if err := WriteFile(path, []byte("new\n"), tt.mode, tt.sync); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "new\n" {
				t.Fatalf("contents = %q, error = %v", got, err)
			}
			got, err = io.ReadAll(old)
			if err != nil || string(got) != "old" {
				t.Fatalf("original file = %q, error = %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tt.mode {
				t.Fatalf("mode = %o, want %o", info.Mode().Perm(), tt.mode)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("directory entries = %v, error = %v", entries, err)
			}
		})
	}
}

func TestWriteFileCleansUpAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("data"), 0o600, false); err == nil {
		t.Fatal("expected error replacing a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "target" {
		t.Fatalf("directory entries = %v, error = %v", entries, err)
	}
}

func TestWriteFileDoesNotCreateParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state")
	if err := WriteFile(path, []byte("data"), 0o600, false); !os.IsNotExist(err) {
		t.Fatalf("expected missing directory error, got %v", err)
	}
}

func TestWriteExecutableDuringConcurrentForks(t *testing.T) {
	stop := make(chan struct{})
	ready := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			ready <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
					if err := exec.Command("/bin/true").Run(); err != nil {
						t.Errorf("background exec: %v", err)
						return
					}
				}
			}
		})
	}
	defer func() { close(stop); workers.Wait() }()
	for range 4 {
		<-ready
	}
	dir := t.TempDir()
	// Exercise writes while four independent fork loops compete for ForkLock.
	// A bounded sample keeps this regression test within the test budget.
	for i := range 8 {
		path := filepath.Join(dir, fmt.Sprintf("script-%d", i))
		if err := WriteExecutable(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command(path).Run(); err != nil {
			t.Fatalf("exec round %d: %v", i, err)
		}
	}
}
