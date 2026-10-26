//go:build linux

// Package runprocs ends the process groups a workflow run started. A run's
// workers carry DYNA_WORKER_RUN=<run id>, and some of them (a Pi worker, a
// shell server) run in groups of their own, so the run's own group does not
// reach them (D-185).
package runprocs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/mode"
)

// exitWait bounds how long Kill waits for killed groups to leave the kernel.
const exitWait = 3 * time.Second

// Kill sends SIGKILL to every process group that holds a process stamped with
// runID, and to the group led by leader (the run's own process, which starts
// in its own session; 0 for none). It then waits for those processes to exit.
// It never signals the caller's own group, a group in keep, or a group that
// holds a process of a job in keepJobs (assignments such as
// "DYNA_SHELL_JOB=<id>" for jobs that outlive the run). One pass over /proc
// decides both, so a kept process that changes group afterwards is never
// selected. It touches only processes that carry the run's stamp.
func Kill(runID string, leader int, keep map[int]bool, keepJobs []string) error {
	if runID == "" {
		return errors.New("a run id is required")
	}
	own, err := syscall.Getpgid(0)
	if err != nil {
		return err
	}
	groups := map[int]bool{}
	if leader > 1 {
		groups[leader] = true
	}
	kept := map[int]bool{}
	for g := range keep {
		kept[g] = true
	}
	stamp := mode.EnvWorkerRun + "=" + runID
	forEachProcess(func(pid int, env []byte) {
		st, ok := readStat(pid)
		if !ok || st.state == 'Z' {
			return
		}
		for _, job := range keepJobs {
			if hasAssignment(env, job) {
				kept[st.group] = true
			}
		}
		if hasAssignment(env, stamp) {
			groups[st.group] = true
		}
	})
	var errs []error
	for g := range groups {
		if g <= 1 || g == own || kept[g] {
			delete(groups, g)
			continue
		}
		if err := syscall.Kill(-g, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, fmt.Errorf("kill process group %d: %w", g, err))
		}
	}
	for deadline := time.Now().Add(exitWait); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if !anyLive(groups, own) {
			return errors.Join(errs...)
		}
	}
	return errors.Join(append(errs, errors.New("run processes did not exit"))...)
}

type procStat struct {
	state byte
	group int
}

func readStat(pid int) (procStat, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStat{}, false
	}
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 {
		return procStat{}, false
	}
	f := strings.Fields(string(b[end+1:]))
	if len(f) < 3 {
		return procStat{}, false
	}
	group, err := strconv.Atoi(f[2])
	if err != nil {
		return procStat{}, false
	}
	return procStat{state: f[0][0], group: group}, true
}

// anyLive reports whether a non-zombie process remains in one of the groups.
// A zombie is dead; its parent has only to reap it.
func anyLive(groups map[int]bool, own int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if st, ok := readStat(pid); ok && st.state != 'Z' && st.group != own && groups[st.group] {
			return true
		}
	}
	return false
}

// Stamped returns the processes, other than the caller, whose environment
// holds the exact assignment "NAME=value".
func Stamped(assignment string) []int {
	var pids []int
	forEachProcess(func(pid int, env []byte) {
		if hasAssignment(env, assignment) {
			pids = append(pids, pid)
		}
	})
	return pids
}

// forEachProcess calls fn with each readable process, other than the caller,
// and its environment.
func forEachProcess(fn func(pid int, env []byte)) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
			fn(pid, env)
		}
	}
}

func hasAssignment(env []byte, assignment string) bool {
	return bytes.Contains(append([]byte{0}, env...), []byte("\x00"+assignment+"\x00"))
}
