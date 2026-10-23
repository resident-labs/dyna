package processguard

import (
	"golang.org/x/sys/unix"
	"testing"
)

func TestPeerAliveRetriesInterruptedPoll(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events int16
		err    error
		want   bool
	}{
		{name: "live", want: true},
		{name: "exited", events: unix.POLLIN},
		{name: "invalid", err: unix.EBADF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := peerAlive(42, func(fds []unix.PollFd, timeout int) (int, error) {
				calls++
				if len(fds) != 1 || fds[0].Fd != 42 || fds[0].Events != unix.POLLIN || timeout != 0 {
					t.Fatalf("unexpected poll arguments: %v, %d", fds, timeout)
				}
				if calls <= 2 {
					return -1, unix.EINTR
				}
				fds[0].Revents = tc.events
				if tc.events != 0 {
					return 1, nil
				}
				return 0, tc.err
			})
			if got != tc.want || calls != 3 {
				t.Fatalf("alive = %v after %d calls; want %v after 3", got, calls, tc.want)
			}
		})
	}
}
