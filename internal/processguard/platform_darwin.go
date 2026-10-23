//go:build darwin

package processguard

import (
	"fmt"
	"golang.org/x/sys/unix"
	"syscall"
	"unsafe"
)

func disableCoreDumps() error {
	return unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
}

func defaultSignal(sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return nil
	}
	action := struct {
		handler, trampoline uintptr
		mask                uint32
		flags               int32
	}{}
	_, _, e := syscall.RawSyscall(syscall.SYS_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&action)), 0)
	if e != 0 {
		return e
	}
	return nil
}

// Darwin has no subreaper or Linux parent-death signal. The control socket EOF
// provides crash notification; kernel process inspection provides drain proof.
func becomeSubreaper() error { return nil }
func reapOrphans(int) int    { return 0 }
func reapGroup(int)          {} // Adopted grandchildren belong to the system reaper.
func processBirth(pid int) (string, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec), nil
}
func groupLive(group int) (bool, error) {
	ps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return true, err
	}
	for _, p := range ps {
		if int(p.Eproc.Pgid) == group && p.Proc.P_stat != 5 {
			return true, nil
		}
	}
	return false, nil
}
