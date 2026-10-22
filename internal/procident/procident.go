// Package procident reads a process's identity from /proc: its parent and its
// start time. A kernel reuses a PID once its process exits, so a recorded PID
// names the same process only while its start time also matches.
package procident

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	b, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procRoot, pid))
	if err != nil {
		return Stat{}, err
	}
	// The command name in parentheses can hold spaces and parentheses, so the
	// fields count from the last ")": state is field 3, the parent field 4 and
	// the start time field 22.
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 {
		return Stat{}, errors.New("invalid process stat: missing command name")
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 {
		return Stat{}, errors.New("invalid process stat: missing start time")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return Stat{}, fmt.Errorf("parent pid: %w", err)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Stat{}, fmt.Errorf("start time: %w", err)
	}
	return Stat{PID: pid, ParentPID: parent, StartTime: start}, nil
}
