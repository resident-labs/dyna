//go:build darwin

package processguard

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Darwin has no PR_GET_DUMPABLE or systemd journal. The child verifies both
// RLIMIT_CORE values, and the parent checks wait status and core artifacts.
func dumpableForTest() (int, error)                                    { return 0, nil }
func dieWithParent()                                                   {}
func assertNoCrashEvents(t *testing.T, started time.Time, pids ...int) {}

// awaitLeaderExit waits for the group leader's exit through kqueue, which
// reports the same event a pidfd poll does on Linux.
func awaitLeaderExit(t *testing.T, ref Reference) {
	t.Helper()
	leader, _ := ref.Leader()
	kq, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(kq)
	change := unix.Kevent_t{Ident: uint64(leader), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE, Fflags: unix.NOTE_EXIT}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return // already gone
		}
		t.Fatal(err)
	}
	events := make([]unix.Kevent_t, 1)
	timeout := unix.Timespec{Sec: 8}
	for {
		n, err := unix.Kevent(kq, nil, events, &timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("group leader %d: %v", leader, err)
		}
		if n == 0 {
			t.Fatalf("group leader %d did not exit", leader)
		}
		return
	}
}

func procFieldsForTest(pid int) (string, error) {
	p, e := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if e != nil {
		return "", e
	}
	if p.Proc.P_stat == 5 {
		return "Z", nil
	}
	if p.Proc.P_pid == 0 {
		return "X", nil
	}
	return "live", nil
}
