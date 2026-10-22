package runstore

import (
	"errors"
	"syscall"

	"dyna-agent/internal/procident"
)

// Alive reports whether the process a run recorded is still the same process.
// A run's PID alone is not enough: after a reboot the kernel hands it to an
// unrelated process, so the recorded start time must match too.
func Alive(m Meta) bool {
	if m.Pid <= 0 {
		return false
	}
	if m.PidStart != 0 {
		if started, err := procident.StartTime(m.Pid); err == nil {
			return started == m.PidStart
		}
	}
	// TODO: legacy/shim remove later — a run recorded without a process start
	// time, or a system without /proc, keeps the plain signal-0 probe.
	return processAlive(m.Pid, syscall.Kill)
}

// processAlive reports whether pid exists, without checking which process it
// is. Callers that can record a start time use Alive instead.
func processAlive(pid int, signal func(int, syscall.Signal) error) bool {
	if pid <= 0 {
		return false
	}
	err := signal(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
