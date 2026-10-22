//go:build darwin

package processguard

import (
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

// Darwin has no PR_GET_DUMPABLE or systemd journal. The child verifies both
// RLIMIT_CORE values, and the parent checks wait status and core artifacts.
func dumpableForTest() (int, error)                                    { return 0, nil }
func assertNoCrashEvents(t *testing.T, started time.Time, pids ...int) {}

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
