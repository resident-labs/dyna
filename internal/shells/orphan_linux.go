package shells

import (
	"syscall"
	"time"

	"dyna-agent/internal/runprocs"
)

// orphanPids returns the live processes that carry the job's DYNA_SHELL_JOB
// stamp. A job whose supervisor died without an exit record can still have
// them, and they are the only processes that surely belong to the job.
func orphanPids(id string) []int {
	return runprocs.Stamped(JobEnv + "=" + id)
}

// stopOrphans ends the job's stamped processes with SIGTERM, then SIGKILL after
// grace. A stamped process that leads a group takes its whole group, so the
// children that the command started (ssh, flock) end with it (D-190).
// It reports whether any process was found.
func stopOrphans(id string, grace time.Duration) bool {
	pids := orphanPids(id)
	if len(pids) == 0 {
		return false
	}
	signal := func(sig syscall.Signal) {
		for _, pid := range pids {
			if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
				_ = syscall.Kill(-pid, sig)
			}
			_ = syscall.Kill(pid, sig)
		}
	}
	signal(syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for len(orphanPids(id)) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if left := orphanPids(id); len(left) > 0 {
		pids = left
		signal(syscall.SIGKILL)
	}
	return true
}
