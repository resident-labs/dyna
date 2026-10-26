package syswait

import (
	"testing"

	"golang.org/x/sys/unix"
)

// D-157: a signal that interrupts the wait must not fail it. The fake poll
// reports one interrupted wait, as a signal arriving mid-wait does, and the
// helper must ask again instead of returning the error.
func TestPollRepeatsAnInterruptedWait(t *testing.T) {
	calls := 0
	poll = func([]unix.PollFd, int) (int, error) {
		calls++
		if calls == 1 {
			return -1, unix.EINTR
		}
		return 1, nil
	}
	t.Cleanup(func() { poll = unix.Poll })
	n, err := Poll([]unix.PollFd{{Fd: -1}}, 500)
	if err != nil || n != 1 || calls != 2 {
		t.Fatalf("Poll = %d, %v after %d waits, want the second wait's result", n, err, calls)
	}
}

// Any other error comes back unchanged, so a real failure is not hidden.
func TestPollReturnsOtherErrors(t *testing.T) {
	poll = func([]unix.PollFd, int) (int, error) { return -1, unix.EBADF }
	t.Cleanup(func() { poll = unix.Poll })
	if _, err := Poll(nil, 0); err != unix.EBADF {
		t.Fatalf("Poll error = %v, want EBADF", err)
	}
}
