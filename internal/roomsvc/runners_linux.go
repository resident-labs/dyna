package roomsvc

import (
	"errors"
	"sync"

	"golang.org/x/sys/unix"
)

// runnerHandle is a pidfd: it becomes readable when the process exits.
type runnerHandle struct{ fd int }

func openRunner(pid int) (runnerHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	return runnerHandle{fd}, err
}

// wait blocks until the runner exits, and reports true, or until quit is
// signalled, and reports false.
func (h runnerHandle) wait(q *quitter) bool {
	quit, err := q.fd()
	if err != nil {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLIN}, {Fd: int32(quit), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, -1)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	return fds[0].Revents != 0 && fds[1].Revents == 0
}

func (h runnerHandle) close() { _ = unix.Close(h.fd) }

// quitter is one eventfd that every watcher polls with its pidfd. Writing to
// it wakes them all, and it stays readable, so a watcher that starts after
// the signal returns at once.
type quitter struct {
	once sync.Once
	efd  int
	err  error
}

func (q *quitter) fd() (int, error) {
	q.once.Do(func() { q.efd, q.err = unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK) })
	return q.efd, q.err
}

func (q *quitter) signal() {
	if fd, err := q.fd(); err == nil {
		_, _ = unix.Write(fd, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	}
}

func (q *quitter) close() {
	if fd, err := q.fd(); err == nil {
		_ = unix.Close(fd)
	}
}
