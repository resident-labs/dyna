// Package syswait waits on a process without failing on a signal.
package syswait

import (
	"time"

	"golang.org/x/sys/unix"
)

// poll is the syscall the wait uses. Tests replace it to deliver an interrupted
// wait without depending on a signal arriving at the right moment.
var poll = unix.Poll

// now is the clock a bounded wait reads. Tests replace it to spend the budget
// without sleeping.
var now = time.Now

// Poll waits until one of fds is ready or timeout expires, and repeats the wait
// after EINTR. A signal that arrives during the wait, such as the runtime's own
// preemption signal on a loaded host, must not turn a healthy child into a
// failed check or a half-finished kill (D-157).
//
// An interrupted wait keeps the budget it started with: a positive timeout
// becomes one deadline, and each repeat asks only for the time left, so repeated
// signals cannot hold a bounded wait open. When that budget runs out with no
// event, Poll reports no event and no error, as an expired wait does. A zero
// timeout still returns at once and a negative one still waits without a bound.
// Any other error returns as it is.
func Poll(fds []unix.PollFd, timeout int) (int, error) {
	if timeout <= 0 {
		for {
			n, err := poll(fds, timeout)
			if err != unix.EINTR {
				return n, err
			}
		}
	}
	deadline := now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		n, err := poll(fds, remainingMillis(deadline))
		if err != unix.EINTR {
			return n, err
		}
		if !now().Before(deadline) {
			return 0, nil
		}
	}
}

// remainingMillis is the wait left before deadline, at least one millisecond so a
// repeat does not spin when less than a millisecond is left.
func remainingMillis(deadline time.Time) int {
	left := deadline.Sub(now())
	if left < time.Millisecond {
		return 1
	}
	return int((left + time.Millisecond - 1) / time.Millisecond)
}
