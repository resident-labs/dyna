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

func (p pinned) signal(sig unix.Signal) {
	_ = unix.PidfdSendSignal(p.fd, sig, nil, 0)
}

// jobProcesses pins the live processes of job id: those that carry its
// DYNA_SHELL_JOB stamp, and every member of their process groups, which holds
// children that dropped the stamp (for example under env -i). Processes in
// have are kept. A pidfd is opened first and the process checked after, so the
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
	// A group is ours to scan while one of its stamped processes is held.
	groups := map[int]bool{}
	for pid := range have {
		if g, ok := processGroup(pid); ok && g > 1 && g != selfGroup {
			groups[g] = true
		}
	}
	if len(groups) == 0 {
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
		if g, ok := processGroup(pid); ok && groups[g] {
			pin(pid, func() bool { g2, ok := processGroup(pid); return ok && g2 == g })
		}
	}
	return have
}

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

// waitExited waits until every process in set has ended or timeout passes. It
// returns the ones still alive.
func waitExited(set map[int]pinned, timeout time.Duration) []pinned {
	deadline := time.Now().Add(timeout)
	for {
		var alive []pinned
		fds := []unix.PollFd{}
		for _, p := range set {
			if !p.exited() {
				alive = append(alive, p)
				fds = append(fds, unix.PollFd{Fd: int32(p.fd), Events: unix.POLLIN})
			}
		}
		left := time.Until(deadline)
		if len(alive) == 0 || left <= 0 {
			return alive
		}
		// Wake when any of them ends.
		_, _ = syswait.Poll(fds, int(left.Milliseconds())+1)
	}
}

// stopOrphans ends the processes of job id: SIGTERM to each, SIGKILL to those
// still alive after grace, then waits for them to end. Every signal goes
// through a pidfd, and group members are found again before SIGKILL, so a
// child that ignores SIGTERM dies even when its stamped leader already left
// (D-190). It reports whether any process was found. An error means a process
// outlived SIGKILL.
func stopOrphans(id string, grace time.Duration) (bool, error) {
	set := jobProcesses(id, map[int]pinned{})
	defer func() {
		for _, p := range set {
			_ = unix.Close(p.fd)
		}
	}()
	if len(set) == 0 {
		return false, nil
	}
	for _, p := range set {
		p.signal(unix.SIGTERM)
	}
	if len(waitExited(set, grace)) == 0 {
		return true, nil
	}
	set = jobProcesses(id, set)
	for _, p := range set {
		if !p.exited() {
			p.signal(unix.SIGKILL)
		}
	}
	if alive := waitExited(set, 5*time.Second); len(alive) > 0 {
		return true, errors.New("job processes did not end after SIGKILL: first is " + strconv.Itoa(alive[0].pid))
	}
	return true, nil
}
