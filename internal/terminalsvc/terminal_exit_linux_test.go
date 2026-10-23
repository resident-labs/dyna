//go:build linux

package terminalsvc

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// waitProcessExit blocks until pid exits. A pidfd becomes readable when its
// process exits, so a test learns the real exit instead of sampling /proc.
func waitProcessExit(t *testing.T, pid, timeout int) {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	ready, err := unix.Poll(fds, timeout)
	for errors.Is(err, unix.EINTR) {
		ready, err = unix.Poll(fds, timeout)
	}
	if err != nil {
		t.Fatal(err)
	}
	if ready == 0 {
		t.Fatalf("process %d did not exit within %d ms", pid, timeout)
	}
}
