//go:build linux

package processguard

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"dyna-agent/internal/childreaper"
	"golang.org/x/sys/unix"
)

func disableCoreDumps() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

func defaultSignal(sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return nil
	}
	action := struct {
		handler, flags, restorer uintptr
		mask                     uint64
	}{}
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&action)), 0, 8, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

func becomeSubreaper() error { return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) }

// reapOrphans reaps adopted children and keeps anchor waitable. It reports how
// many it reaped, so a caller can publish the reaping.
func reapOrphans(anchor int) int {
	return childreaper.Reap(func(pid int) bool { return pid == anchor })
}

// reapGroup waits for adopted group children to become waitable. A zombie
// leader can still have exiting threads, so groupLive alone cannot prove
// that all owned children have been reaped. Other groups may remain live.
func reapGroup(group int) {
	for {
		_, err := unix.Wait4(-group, nil, 0, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return
		}
	}
}

func procFields(pid int) ([]string, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil, err
	}
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 {
		return nil, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 {
		return nil, errors.New("short proc stat")
	}
	return fields, nil
}
func processBirth(pid int) (string, error) {
	f, err := procFields(pid)
	if err != nil {
		return "", err
	}
	return f[19], nil
}
func groupLive(group int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true, err
	}
	want := strconv.Itoa(group)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		f, err := procFields(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return true, err
		}
		if f[2] == want && f[0] != "Z" && f[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
