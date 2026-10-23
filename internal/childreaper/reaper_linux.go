//go:build linux

// Package childreaper drains exited children without consuming owned statuses.
package childreaper

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux siginfo_t places the SIGCHLD union after three int32 fields, rounded
// to native word alignment. x/sys keeps that union private.
func childPID(info *unix.Siginfo) int {
	offset := uintptr(12)
	alignment := unsafe.Alignof(uintptr(0))
	offset = (offset + alignment - 1) &^ (alignment - 1)
	return int(*(*int32)(unsafe.Add(unsafe.Pointer(info), offset)))
}

// Reap drains available exit statuses until none remain or keep returns true.
// A kept child stays waitable for its owner. Callers must serialize this call
// with child registration and other waits for children that are not kept.
// It returns how many children it reaped.
func Reap(keep func(int) bool) int {
	reaped := 0
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_ALL, 0, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return reaped
		}
		pid := childPID(&info)
		if pid == 0 || keep(pid) {
			return reaped
		}
		n, err := unix.Wait4(pid, nil, unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil && err != unix.ECHILD {
			return reaped
		}
		if n == pid {
			reaped++
		}
	}
}
