package admission

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MeasureTurns returns the average resident memory, in bytes, of the process
// trees of the given runner processes: the memory one running turn really uses
// on this machine. It reads /proc under procDir and returns 0 when it measured
// nothing, so the caller keeps its fallback.
func MeasureTurns(procDir string, pids []int, pageSize uint64) uint64 {
	children, rss := processTable(procDir, pageSize)
	var total, counted uint64
	for _, pid := range pids {
		if _, ok := rss[pid]; !ok {
			continue
		}
		total += treeRSS(pid, children, rss)
		counted++
	}
	if counted == 0 {
		return 0
	}
	return total / counted
}

func treeRSS(pid int, children map[int][]int, rss map[int]uint64) uint64 {
	sum := rss[pid]
	for _, child := range children[pid] {
		sum += treeRSS(child, children, rss)
	}
	return sum
}

// processTable lists each process's children and resident bytes from
// /proc/<pid>/stat and /proc/<pid>/statm.
func processTable(procDir string, pageSize uint64) (map[int][]int, map[int]uint64) {
	children, rss := map[int][]int{}, map[int]uint64{}
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return children, rss
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procDir, e.Name(), "stat"))
		if err != nil {
			continue
		}
		// The command name sits in parentheses and may hold spaces, so the
		// fields start after the last closing parenthesis: state, then ppid.
		text := string(stat)
		after := text[strings.LastIndexByte(text, ')')+1:]
		fields := strings.Fields(after)
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		statm, err := os.ReadFile(filepath.Join(procDir, e.Name(), "statm"))
		if err != nil {
			continue
		}
		if f := strings.Fields(string(statm)); len(f) >= 2 {
			if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				rss[pid] = pages * pageSize
			}
		}
	}
	return children, rss
}

// Sampler measures the memory one running turn uses, from the runner processes
// that hold slots.
type Sampler interface {
	// Measure returns the average resident bytes of the runners' process trees,
	// or 0 when it measured nothing.
	Measure(ctx context.Context, pids []int) uint64
}

// ProcSampler measures through /proc (Linux).
type ProcSampler struct{ ProcDir string }

// Measure implements Sampler.
func (p ProcSampler) Measure(_ context.Context, pids []int) uint64 {
	return MeasureTurns(p.ProcDir, pids, uint64(os.Getpagesize()))
}

// PSSampler measures through `ps` (macOS and other Unix systems with no /proc).
type PSSampler struct {
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Measure implements Sampler.
func (p PSSampler) Measure(ctx context.Context, pids []int) uint64 {
	out, err := p.Run(ctx, "ps", "-axo", "pid=,ppid=,rss=")
	if err != nil {
		return 0
	}
	return MeasurePS(out, pids)
}

// MeasurePS averages the process-tree resident bytes of pids from `ps` output
// with one "pid ppid rss-in-KiB" line per process.
func MeasurePS(out []byte, pids []int) uint64 {
	children, rss := map[int][]int{}, map[int]uint64{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		kb, e3 := strconv.ParseUint(f[2], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		rss[pid] = kb << 10
	}
	var total, counted uint64
	for _, pid := range pids {
		if _, ok := rss[pid]; ok {
			total += treeRSS(pid, children, rss)
			counted++
		}
	}
	if counted == 0 {
		return 0
	}
	return total / counted
}
