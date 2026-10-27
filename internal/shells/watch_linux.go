//go:build linux

package shells

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// DirWatch wakes when a file in a watched directory is created or written,
// or when Wake is called. It serves wait loops outside this package too,
// such as the shell server's steering watch.
type DirWatch struct {
	inotify, event int
	// mu orders Wake and Close: a wake after close must not write to a
	// descriptor number that something else has reused.
	mu     sync.Mutex
	closed bool
}

// WatchDir watches dir for writes and creations of the files inside it.
func WatchDir(dir string) (*DirWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_MODIFY|unix.IN_CREATE|unix.IN_MOVED_TO|unix.IN_CLOSE_WRITE); err != nil {
		unix.Close(fd)
		return nil, err
	}
	event, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &DirWatch{inotify: fd, event: event}, nil
}

// Next returns after a change, a wake, or d, whichever comes first.
func (w *DirWatch) Next(d time.Duration) error {
	fds := []unix.PollFd{{Fd: int32(w.inotify), Events: unix.POLLIN}, {Fd: int32(w.event), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(d.Milliseconds())+1)
	if err != nil && !errors.Is(err, unix.EINTR) {
		return err
	}
	if n > 0 {
		var buf [4096]byte
		for {
			if _, err := unix.Read(w.inotify, buf[:]); err != nil {
				break
			}
		}
	}
	return nil
}

// Wake makes the current or next call to Next return at once.
func (w *DirWatch) Wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		_, _ = unix.Write(w.event, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	}
}

// Close releases the watch's descriptors.
func (w *DirWatch) Close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	unix.Close(w.inotify)
	unix.Close(w.event)
}
