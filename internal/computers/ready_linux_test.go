//go:build linux

package computers

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// readyFIFO creates the FIFO a started child writes to. A child's write blocks
// until the test reads it, so the test waits for a real start event instead of
// polling a marker or the process table.
func readyFIFO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ready")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// awaitReady blocks until the child's ready write arrives. The timeout only
// bounds a real hang, so it is not a poll interval.
func awaitReady(t *testing.T, path string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			done <- err
			return
		}
		defer f.Close()
		_, err = io.ReadFull(f, make([]byte, len("ready")))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child never signalled ready: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("child never signalled ready")
	}
}
