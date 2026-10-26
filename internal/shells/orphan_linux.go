package shells

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
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
	pid   int
	fd    int
	group int // its process group when pinned, or 0 if unknown
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

func (p pinned) signal(sig unix.Signal) error {
	return unix.PidfdSendSignal(p.fd, sig, nil, 0)
}

// stopped waits until the process is stopped and still holds its number. A
// sent SIGSTOP is not yet a stopped process, and a zombie is no containment: its
// parent may reap it at any time and release its group. It reports whether
// containment is confirmed.
func (p pinned) stopped() bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); runtime.Gosched() {
		state, _, ok := processStat(p.pid)
		if !p.reserved() {
			return false
		}
		if ok && (state == 'Z' || state == 'X') {
			return false
		}
		if ok && (state == 'T' || state == 't') {
			return true
		}
	}
	return false
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
		g, _ := readGroup(pid)
		if !(pinned{pid: pid, fd: fd}).reserved() {
			g = 0
		}
		have[pid] = pinned{pid: pid, fd: fd, group: g}
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

// processGroup reads a process's group.
func processGroup(pid int) (int, bool) {
	_, group, ok := processStat(pid)
	return group, ok
}

// processStat reads a process's state and group from /proc/<pid>/stat.
func processStat(pid int) (state byte, group int, ok bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	// The name in field 2 may hold spaces and parentheses: read after the last ")".
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return 0, 0, false
	}
	var ppid int
	if _, err := fmt.Sscanf(string(raw[i+1:]), " %c %d %d", &state, &ppid, &group); err != nil {
		return 0, 0, false
	}
	return state, group, true
}

// beforeFreeze runs after the first scan of stopOrphans; a test uses it to
// change the processes at that exact point.
var beforeFreeze func()

// beforeStop and afterContain run just before a process is sent SIGSTOP and
// after its containment check; a test uses them to change the processes at
// those exact points.
var beforeStop, afterContain func(pid int)

// stopOrphans ends the processes of job id. It first stops every process with
// SIGSTOP, confirms each is stopped, and scans the job's groups again until a
// scan adds nothing: a stopped process can neither fork nor exit, so no member
// is lost and no group number is released. Then it sends SIGKILL through the
// pidfds and waits until all have ended (D-190). The supervisor is gone, so
// nothing waits for a clean exit. It reports whether any process was found.
// An error means a process outlived SIGKILL, or a process ended before it was
// contained and no stopped member kept its group, so a child it started may
// still run.
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
	if beforeFreeze != nil {
		beforeFreeze()
	}
	frozen, uncontained := map[int]bool{}, map[int]bool{}
	for added := true; added; {
		added = false
		set = jobProcesses(id, set)
		for _, p := range set {
			if !frozen[p.pid] {
				frozen[p.pid] = true
				added = true
				if beforeStop != nil {
					beforeStop(p.pid)
				}
				if p.signal(unix.SIGSTOP) != nil || !p.stopped() {
					uncontained[p.pid] = true
				}
				if afterContain != nil {
					afterContain(p.pid)
				}
			}
		}
	}
	var incomplete error
	for pid := range uncontained {
		covered := false
		for _, q := range set {
			covered = covered || !uncontained[q.pid] && q.group > 1 && q.group == set[pid].group && q.reserved()
		}
		if !covered {
			incomplete = fmt.Errorf("process %d ended before it was contained; a child it started may still run", pid)
		}
	}
	for _, p := range set {
		_ = p.signal(unix.SIGKILL)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, p := range set {
		for !p.exited() {
			left := time.Until(deadline)
			if left <= 0 {
				return true, errors.Join(incomplete, errors.New("job processes did not end after SIGKILL: first is "+strconv.Itoa(p.pid)))
			}
			_, _ = syswait.Poll([]unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}, int(left.Milliseconds())+1)
		}
	}
	return true, incomplete
}
