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
// It never signals the caller's own group, and it touches only processes that
// carry the run's stamp.
func Kill(runID string, leader int) error {
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
	for _, pid := range Stamped(mode.EnvWorkerRun + "=" + runID) {
		if st, ok := readStat(pid); ok && st.state != 'Z' {
			groups[st.group] = true
		}
	}
	var errs []error
	for g := range groups {
		if g <= 1 || g == own {
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
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	stamp := []byte("\x00" + assignment + "\x00")
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err == nil && bytes.Contains(append([]byte{0}, env...), stamp) {
			pids = append(pids, pid)
		}
	}
	return pids
}
