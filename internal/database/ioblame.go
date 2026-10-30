package database

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A slow checkpoint moves few frames, so the time goes to the filesystem
// waiting on other writers. These helpers name them. They read /proc once, when
// a checkpoint is slow, and compare with the reading from the previous slow
// checkpoint. Nothing samples in the background.

// topWriters is how many processes the log line names.
const topWriters = 3

// procRoot is replaced by tests.
var procRoot = "/proc"

// ioReading is what /proc held at one slow checkpoint.
type ioReading struct {
	stallMicros uint64 // total microseconds some task stalled on IO
	written     map[int]procWrite
}

type procWrite struct {
	name  string
	bytes uint64
}

// lastIO is the reading from the previous slow checkpoint. Only the engine's
// checkpoint loop writes it.
var lastIO *ioReading

// readIO reads the IO stall total and each readable process's write_bytes.
// Processes it may not read are left out.
func readIO() ioReading {
	r := ioReading{written: map[int]procWrite{}}
	if f, err := os.Open(filepath.Join(procRoot, "pressure", "io")); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			if rest, ok := strings.CutPrefix(s.Text(), "some "); ok {
				for _, field := range strings.Fields(rest) {
					if v, ok := strings.CutPrefix(field, "total="); ok {
						r.stallMicros, _ = strconv.ParseUint(v, 10, 64)
					}
				}
			}
		}
		f.Close()
	}
	dirs, _ := filepath.Glob(filepath.Join(procRoot, "[0-9]*"))
	for _, dir := range dirs {
		pid, err := strconv.Atoi(filepath.Base(dir))
		if err != nil {
			continue
		}
		io, err := os.ReadFile(filepath.Join(dir, "io"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(io), "\n") {
			if v, ok := strings.CutPrefix(line, "write_bytes: "); ok {
				n, _ := strconv.ParseUint(v, 10, 64)
				comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
				r.written[pid] = procWrite{name: strings.TrimSpace(string(comm)), bytes: n}
			}
		}
	}
	return r
}

// ioBlame reads /proc and describes what changed since the previous slow
// checkpoint: the IO stall time and the processes that wrote the most. The
// first call has nothing to compare with and reports that.
func ioBlame() string {
	cur := readIO()
	prev := lastIO
	lastIO = &cur
	if prev == nil {
		return "io: first slow checkpoint, no earlier reading"
	}
	type delta struct {
		pid   int
		name  string
		bytes uint64
	}
	// A writer that exited since the last reading leaves no write_bytes to
	// read, so it cannot be ranked. The line reports how many were lost.
	exited := 0
	for pid := range prev.written {
		if _, ok := cur.written[pid]; !ok {
			exited++
		}
	}
	var top []delta
	for pid, w := range cur.written {
		// A process missing from the earlier reading started since then.
		if w.bytes > prev.written[pid].bytes {
			top = append(top, delta{pid, w.name, w.bytes - prev.written[pid].bytes})
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].bytes > top[j].bytes })
	if len(top) > topWriters {
		top = top[:topWriters]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "io: stalled %d ms since the last slow checkpoint; top visible writers:", (cur.stallMicros-prev.stallMicros)/1000)
	if len(top) == 0 {
		b.WriteString(" none")
	}
	for _, d := range top {
		fmt.Fprintf(&b, " %s[%d] %d KB", d.name, d.pid, d.bytes>>10)
	}
	fmt.Fprintf(&b, "; %d earlier processes exited and are not ranked", exited)
	return b.String()
}
