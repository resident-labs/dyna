//go:build linux

package shells

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestWakeAfterCloseWritesNothing(t *testing.T) {
	w, err := WatchDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := w.event
	w.Close()
	// Linux reuses the lowest free descriptors: close freed the inotify
	// and event descriptors, so two new eventfds take both numbers.
	var reused int = -1
	for range 2 {
		fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if fd == old {
			reused = fd
		}
	}
	if reused < 0 {
		t.Fatalf("descriptor %d was not reused", old)
	}
	// A cancellation callback that starts after Wait returned.
	w.Wake()
	var buf [8]byte
	if _, err := unix.Read(reused, buf[:]); err != unix.EAGAIN {
		t.Fatalf("wake after close wrote to a reused descriptor: %v %v", buf, err)
	}
}
