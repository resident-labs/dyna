package shells

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/runprocs"
	"dyna-agent/internal/syswait"
)

// pinned is a process held by a pidfd. Its number cannot name another process
// while the pidfd is open, so a signal through it cannot hit unrelated work.
type pinned struct {
	pid int
	fd  int
}

// exited reports whether the process has ended (a zombie counts as ended).
func (p pinned) exited() bool {
	n, err := syswait.Poll([]unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}, 0)
	return err == nil && n > 0
}

// reserved reports whether the process still holds its number: it runs, or it
// is a zombie its parent has not reaped. After that the number may name another
// process, so nothing read from /proc by number may be trusted.
func (p pinned) reserved() bool {
	return unix.PidfdSendSignal(p.fd, 0, nil, 0) == nil
}

func (p pinned) signal(sig unix.Signal) {
	_ = unix.PidfdSendSignal(p.fd, sig, nil, 0)
}

// jobProcesses pins the live processes of job id: those that carry its
// DYNA_SHELL_JOB stamp, and every member of their process groups, which holds
// children that dropped the stamp (for example under env -i). Processes in
// have are kept. A group is read only from a process that still holds its
// number, checked before and after the read. A pidfd is opened first and the process checked after, so the
// check cannot describe a different process that reused the number.
func jobProcesses(id string, have map[int]pinned) map[int]pinned {
	assignment := []byte("\x00" + JobEnv + "=" + id + "\x00")
	self, selfGroup := os.Getpid(), syscall.Getpgrp()
	pin := func(pid int, ok func() bool) {
		if _, kept := have[pid]; kept || pid == self {
			return
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			return
		}
		if !ok() {
			_ = unix.Close(fd)
			return
		}
		have[pid] = pinned{pid: pid, fd: fd}
	}
	for _, pid := range runprocs.Stamped(JobEnv + "=" + id) {
		pin(pid, func() bool {
			env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			return err == nil && bytes.Contains(append([]byte{0}, env...), assignment)
		})
	}
	// A group is ours to scan while one of its held processes, its anchor,
	// still holds its number. The anchor is kept with the group and checked
	// again after each candidate's group is read.
	anchors := map[int]pinned{}
	for pid, p := range have {
		if !p.reserved() {
			continue
		}
		if g, ok := readGroup(pid); ok && p.reserved() && g > 1 && g != selfGroup {
			anchors[g] = p
		}
	}
	if len(anchors) == 0 {
		return have
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return have
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if g, ok := readGroup(pid); ok && anchors[g].fd != 0 {
			anchor := anchors[g]
			pin(pid, func() bool { g2, ok := readGroup(pid); return ok && g2 == g && anchor.reserved() })
		}
	}
	return have
}

// readGroup is processGroup; a test replaces it to simulate a reused number.
var readGroup = processGroup

// processGroup reads a process's group from /proc/<pid>/stat.
func processGroup(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// The name in field 2 may hold spaces and parentheses: read after the last ")".
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return 0, false
	}
	var state byte
	var ppid, group int
	if _, err := fmt.Sscanf(string(raw[i+1:]), " %c %d %d", &state, &ppid, &group); err != nil {
		return 0, false
	}
	return group, true
}

// stopOrphans ends the processes of job id. It first stops every process with
// SIGSTOP and scans the job's groups again until a scan adds nothing: a stopped
// process can neither fork nor exit, so no member is lost or reaped meanwhile
// and no group number is released. Then it sends SIGKILL through the pidfds and
// waits until all have ended (D-190). The supervisor is gone, so nothing waits
// for a clean exit. It reports whether any process was found. An error means a
// process outlived SIGKILL.
func stopOrphans(id string) (bool, error) {
	set := jobProcesses(id, map[int]pinned{})
	defer func() {
		for _, p := range set {
			_ = unix.Close(p.fd)
		}
	}()
	if len(set) == 0 {
		return false, nil
	}
	frozen := map[int]bool{}
	for added := true; added; {
		added = false
		set = jobProcesses(id, set)
		for _, p := range set {
			if !frozen[p.pid] {
				frozen[p.pid] = true
				added = true
				p.signal(unix.SIGSTOP)
			}
		}
	}
	for _, p := range set {
		p.signal(unix.SIGKILL)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, p := range set {
		for !p.exited() {
			left := time.Until(deadline)
			if left <= 0 {
				return true, errors.New("job processes did not end after SIGKILL: first is " + strconv.Itoa(p.pid))
			}
			_, _ = syswait.Poll([]unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}, int(left.Milliseconds())+1)
		}
	}
	return true, nil
}
