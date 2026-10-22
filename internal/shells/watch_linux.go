//go:build linux

package shells

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// dirWatch wakes when a file in a job directory is created or written, or
// when wake is called.
type dirWatch struct {
	inotify, event int
	// mu orders wake and close: a wake after close must not write to a
	// descriptor number that something else has reused.
	mu     sync.Mutex
	closed bool
}

func watchDir(dir string) (*dirWatch, error) {
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
	return &dirWatch{inotify: fd, event: event}, nil
}

// next returns after a change, a wake, or d, whichever comes first.
func (w *dirWatch) next(d time.Duration) error {
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

// wake makes the current or next call to next return at once.
func (w *dirWatch) wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		_, _ = unix.Write(w.event, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	}
}

func (w *dirWatch) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	unix.Close(w.inotify)
	unix.Close(w.event)
}
