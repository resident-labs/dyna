package syswait

import (
	"testing"
	"time"

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
	n, err := Poll([]unix.PollFd{{Fd: -1}}, -1)
	if err != nil || n != 1 || calls != 2 {
		t.Fatalf("Poll = %d, %v after %d waits, want the second wait's result", n, err, calls)
	}
}

// D-157 review: a bounded wait keeps its budget. Interruptions do not restart the
// timeout, so repeated signals cannot hold a five-second wait open forever.
func TestPollKeepsTheBudgetAcrossInterrupts(t *testing.T) {
	clock := time.Unix(1700000000, 0)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	var asked []int
	poll = func(_ []unix.PollFd, timeout int) (int, error) {
		asked = append(asked, timeout)
		clock = clock.Add(400 * time.Millisecond)
		return -1, unix.EINTR
	}
	t.Cleanup(func() { poll = unix.Poll })
	n, err := Poll([]unix.PollFd{{Fd: -1}}, 1000)
	if err != nil || n != 0 {
		t.Fatalf("Poll = %d, %v, want an expired wait without an error", n, err)
	}
	if want := []int{1000, 600, 200}; len(asked) != len(want) || asked[0] != want[0] || asked[1] != want[1] || asked[2] != want[2] {
		t.Fatalf("waits asked for %v, want the shrinking budget %v", asked, want)
	}
}

// A zero timeout still returns at once, and a negative one keeps no deadline.
func TestPollKeepsUnboundedAndImmediateWaits(t *testing.T) {
	calls := 0
	var asked []int
	poll = func(_ []unix.PollFd, timeout int) (int, error) {
		calls++
		asked = append(asked, timeout)
		if calls == 1 {
			return -1, unix.EINTR
		}
		return 1, nil
	}
	t.Cleanup(func() { poll = unix.Poll })
	if n, err := Poll(nil, 0); err != nil || n != 1 || asked[0] != 0 || asked[1] != 0 {
		t.Fatalf("zero timeout asked for %v, got %d %v", asked, n, err)
	}
	calls, asked = 0, nil
	if n, err := Poll(nil, -5); err != nil || n != 1 || len(asked) != 2 || asked[0] != -5 || asked[1] != -5 {
		t.Fatalf("negative timeout asked for %v, got %d %v", asked, n, err)
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
