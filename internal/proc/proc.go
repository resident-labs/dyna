// Package proc reads Linux process inventory from a /proc tree.
package proc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Process is one process's identity, CPU use and resident memory.
type Process struct {
	PID       int
	ParentPID int
	Command   string
	State     string
	// StartTime is field 22 of /proc/<pid>/stat, in clock ticks after boot.
	StartTime uint64
	// CPUTicks is the sum of user and system CPU time.
	CPUTicks uint64
	// Memory is resident memory in bytes. It is zero when statm is unavailable.
	Memory uint64
}

// Read reads one process's stat and statm files from procRoot.
func Read(procRoot string, pid int) (Process, error) {
	p, err := ReadStat(procRoot, pid)
	if err != nil {
		return Process{}, err
	}
	p.Memory = residentMemory(procRoot, pid)
	return p, nil
}

// ReadStat reads one process's stat file from procRoot without reading statm.
func ReadStat(procRoot string, pid int) (Process, error) {
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return Process{}, err
	}
	// The command name in parentheses can hold spaces and parentheses, so the
	// fields count from the last ")": state is field 3 and start time field 22.
	s := string(stat)
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return Process{}, errors.New("invalid process stat: missing command name")
	}
	fields := strings.Fields(s[end+1:])
	if len(fields) < 20 {
		return Process{}, errors.New("invalid process stat: missing start time")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, fmt.Errorf("parent pid: %w", err)
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return Process{}, fmt.Errorf("user CPU time: %w", err)
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return Process{}, fmt.Errorf("system CPU time: %w", err)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Process{}, fmt.Errorf("start time: %w", err)
	}
	return Process{
		PID:       pid,
		ParentPID: parent,
		Command:   s[open+1 : end],
		State:     fields[0],
		StartTime: start,
		CPUTicks:  utime + stime,
	}, nil
}

// List reads every process and its resident memory below procRoot. Processes
// that leave or have unreadable stat files during the read are absent.
func List(procRoot string) map[int]Process {
	return list(procRoot, Read)
}

// ListStat reads every process's stat file below procRoot without reading
// statm. Processes that leave or have unreadable stat files are absent.
func ListStat(procRoot string) map[int]Process {
	return list(procRoot, ReadStat)
}

func list(procRoot string, read func(string, int) (Process, error)) map[int]Process {
	entries, _ := os.ReadDir(procRoot)
	out := map[int]Process{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := read(procRoot, pid)
		if err == nil {
			out[pid] = p
		}
	}
	return out
}

func residentMemory(procRoot string, pid int) uint64 {
	statm, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "statm"))
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(statm))
	if len(fields) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(fields[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}
