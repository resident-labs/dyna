// Package procident reads a process's identity from /proc: its parent and its
// start time. A kernel reuses a PID once its process exits, so a recorded PID
// names the same process only while its start time also matches.
package procident

import (
	"errors"
	"syscall"

	"dyna-agent/internal/proc"
)

// Stat is the identity of one process.
type Stat struct {
	PID       int
	ParentPID int
	// StartTime is field 22 of /proc/<pid>/stat, in clock ticks after boot.
	StartTime uint64
}

// Read reads pid's identity from procRoot; production code passes "/proc" and
// a test passes its own tree.
func Read(procRoot string, pid int) (Stat, error) {
	p, err := proc.ReadStat(procRoot, pid)
	if err != nil {
		return Stat{}, err
	}
	return Stat{PID: p.PID, ParentPID: p.ParentPID, StartTime: p.StartTime}, nil
}

// Alive reports whether a process exists. A process of another user answers
// with a permission error, which still means it exists.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
