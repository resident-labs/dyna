// Package syswait waits on a process without failing on a signal.
package syswait

import "golang.org/x/sys/unix"

// poll is the syscall the wait uses. Tests replace it to deliver an interrupted
// wait without depending on a signal arriving at the right moment.
var poll = unix.Poll

// Poll waits until one of fds is ready or timeout expires, and repeats the wait
// after EINTR. A signal that arrives during the wait, such as the runtime's own
// preemption signal on a loaded host, must not turn a healthy child into a
// failed check or a half-finished kill (D-157). Any other error returns as it is.
func Poll(fds []unix.PollFd, timeout int) (int, error) {
	for {
		n, err := poll(fds, timeout)
		if err != unix.EINTR {
			return n, err
		}
	}
}
