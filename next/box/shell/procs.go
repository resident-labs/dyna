package shell

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	clockTicks   = 100 // USER_HZ, the unit of utime and stime in /proc/<pid>/stat on Linux
	listedProcs  = 8   // processes named in a running job's status
	commandChars = 160 // characters of each process's command line
)

// procSample is one process of a job's group at one moment.
type procSample struct {
	command string
	cpu     time.Duration // user and system time since it started
	io      uint64        // bytes it read and wrote, through any file, pipe or socket
}

// readGroup samples every process in the process group pgid from /proc. A process that ends while it is read is left out.
func readGroup(pgid int) map[int]procSample {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	group := map[int]procSample{}
	for _, path := range stats {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// The command name in parentheses may hold spaces, so the fields start after the last ')'.
		fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
		if len(fields) < 13 || fields[2] != strconv.Itoa(pgid) {
			continue
		}
		utime, _ := strconv.ParseInt(fields[11], 10, 64)
		stime, _ := strconv.ParseInt(fields[12], 10, 64)
		dir := filepath.Dir(path)
		pid, _ := strconv.Atoi(filepath.Base(dir))
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		command := strings.TrimSpace(string(bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '})))
		if command == "" {
			command = string(data[bytes.IndexByte(data, '(')+1 : bytes.LastIndexByte(data, ')')])
		}
		group[pid] = procSample{command: command, cpu: time.Duration(utime+stime) * time.Second / clockTicks, io: readIO(filepath.Join(dir, "io"))}
	}
	return group
}

// readIO is a process's rchar plus wchar, or 0 when its io file cannot be read.
func readIO(path string) uint64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	var total uint64
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		name, value, _ := strings.Cut(lines.Text(), ": ")
		if name == "rchar" || name == "wchar" {
			n, _ := strconv.ParseUint(value, 10, 64)
			total += n
		}
	}
	return total
}

// compareGroup lists cur's processes, busiest first, and says whether any did work since prev: used CPU, read or wrote, or
// started.
func compareGroup(prev, cur map[int]procSample) (string, bool) {
	type row struct {
		sample procSample
		busy   bool
	}
	rows := make([]row, 0, len(cur))
	active := false
	for pid, sample := range cur {
		before, seen := prev[pid]
		busy := !seen || sample.cpu > before.cpu || sample.io != before.io
		active = active || busy
		rows = append(rows, row{sample, busy})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.busy != b.busy {
			if a.busy {
				return -1
			}
			return 1
		}
		return int(b.sample.cpu - a.sample.cpu)
	})
	var text strings.Builder
	for i, r := range rows {
		if i == listedProcs {
			fmt.Fprintf(&text, "and %d more\n", len(rows)-listedProcs)
			break
		}
		state := "idle"
		if r.busy {
			state = "busy"
		}
		command := r.sample.command
		if len(command) > commandChars {
			command = strings.ToValidUTF8(command[:commandChars], "") + "…"
		}
		fmt.Fprintf(&text, "%s (%s CPU, %s)\n", command, r.sample.cpu.Truncate(time.Second), state)
	}
	return strings.TrimSuffix(text.String(), "\n"), active
}
