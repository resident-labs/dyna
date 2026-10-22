package worktrees

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A fetch helper that outlives a killed git must not hold engine shutdown.
func TestGitOutputCancelDoesNotWaitForChildren(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		// The child keeps git's stdout open, as git-remote-http does during fetch.
		_, err := gitOutput(ctx, dir, "-c", `alias.hang=!f() { echo > "$1"; exec sleep 30; }; f`, "hang", ready)
		done <- err
	}()
	started := make(chan error, 1)
	go func() {
		f, err := os.Open(ready)
		if err == nil {
			_, err = io.ReadAll(f)
			f.Close()
		}
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("git returned before its child started: %v", err)
	}
	cancel()
	start := time.Now()
	if err := <-done; err == nil {
		t.Fatal("cancelled git returned no error")
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("gitOutput returned %s after cancel; want under 1s", d)
	}
}
