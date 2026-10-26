package admission

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Readings is what the machine looks like now, as one source reports it.
// Memory is the smaller of the machine's and the container's: a cgroup limit
// counts, so containers, WSL and systemd slices are judged by what they can use.
type Readings struct {
	Cores    int
	Load1    float64
	MemTotal uint64
	MemAvail uint64
	// PressureCPU and PressureMem are the share of the last 10 seconds in which
	// some task stalled (Linux PSI), or -1 when the source has none.
	PressureCPU float64
	PressureMem float64
	// Source names what was read, for the status the owner sees.
	Source string
}

// Source reads the machine. A source that cannot read returns an error, and the
// gate then lets every turn through.
type Source interface {
	Read(ctx context.Context) (Readings, error)
}

// Detect returns the source for this platform, or nil when there is none.
func Detect() Source {
	switch runtime.GOOS {
	case "linux":
		return Linux{ProcDir: "/proc", CgroupDir: "/sys/fs/cgroup"}
	case "darwin":
		return Darwin{Run: runCommand}
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Linux reads /proc, PSI and the cgroup limits of the process's own cgroup. The
// directories are fields so a test can point them at fixture files.
type Linux struct{ ProcDir, CgroupDir string }

// Read implements Source.
func (l Linux) Read(context.Context) (Readings, error) {
	r := Readings{Cores: runtime.NumCPU(), PressureCPU: -1, PressureMem: -1}
	loadavg, err := os.ReadFile(filepath.Join(l.ProcDir, "loadavg"))
	if err != nil {
		return Readings{}, err
	}
	if _, err := fmt.Sscanf(string(loadavg), "%f", &r.Load1); err != nil {
		return Readings{}, fmt.Errorf("loadavg: %w", err)
	}
	meminfo, err := os.ReadFile(filepath.Join(l.ProcDir, "meminfo"))
	if err != nil {
		return Readings{}, err
	}
	if r.MemTotal, r.MemAvail, err = ParseMeminfo(meminfo); err != nil {
		return Readings{}, err
	}
	r.Source = "linux /proc"
	if v, ok := readPressure(filepath.Join(l.ProcDir, "pressure", "cpu")); ok {
		r.PressureCPU = v
	}
	if v, ok := readPressure(filepath.Join(l.ProcDir, "pressure", "memory")); ok {
		r.PressureMem = v
		r.Source += " + PSI"
	}
	if cg, err := os.ReadFile(filepath.Join(l.ProcDir, "self", "cgroup")); err == nil {
		if version := l.applyCgroup(&r, string(cg)); version != "" {
			r.Source += " + cgroup " + version
		}
	}
	return r, nil
}

// ParseMeminfo returns MemTotal and MemAvailable in bytes. A kernel without
// MemAvailable gets free plus buffers plus cache.
func ParseMeminfo(data []byte) (total, avail uint64, err error) {
	var free, buffers, cached uint64
	haveAvail := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		fields := strings.Fields(rest)
		if !ok || len(fields) == 0 {
			continue
		}
		kb, perr := strconv.ParseUint(fields[0], 10, 64)
		if perr != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = kb << 10
		case "MemAvailable":
			avail, haveAvail = kb<<10, true
		case "MemFree":
			free = kb << 10
		case "Buffers":
			buffers = kb << 10
		case "Cached":
			cached = kb << 10
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("no MemTotal in meminfo")
	}
	if !haveAvail {
		avail = free + buffers + cached
	}
	return total, min(avail, total), scanner.Err()
}

// ParsePressure returns the "some avg10" value of a PSI file.
func ParsePressure(data []byte) (float64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if value, ok := strings.CutPrefix(field, "avg10="); ok {
				v, err := strconv.ParseFloat(value, 64)
				return v, err == nil
			}
		}
	}
	return 0, false
}

func readPressure(file string) (float64, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	return ParsePressure(data)
}

// unlimited is the size above which a cgroup limit means "no limit": cgroup v1
// reports a page-rounded 2^63 and v2 writes "max".
const unlimited = 1 << 60

// applyCgroup narrows r to the memory limits of the process's cgroup and its
// parents, and returns the cgroup version it used, or "" if it found none.
func (l Linux) applyCgroup(r *Readings, selfCgroup string) string {
	version := ""
	for _, line := range strings.Split(selfCgroup, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			for dir := path.Clean(parts[2]); ; dir = path.Dir(dir) {
				base := filepath.Join(l.CgroupDir, dir)
				if limit, ok := readNumber(filepath.Join(base, "memory.max")); ok && limit < unlimited {
					used, _ := readNumber(filepath.Join(base, "memory.current"))
					narrow(r, limit, used)
					version = "v2"
				}
				if dir == "/" || dir == "." {
					break
				}
			}
		case strings.Contains(","+parts[1]+",", ",memory,"):
			for dir := path.Clean(parts[2]); ; dir = path.Dir(dir) {
				base := filepath.Join(l.CgroupDir, "memory", dir)
				if limit, ok := readNumber(filepath.Join(base, "memory.limit_in_bytes")); ok && limit < unlimited {
					used, _ := readNumber(filepath.Join(base, "memory.usage_in_bytes"))
					narrow(r, limit, used)
					version = "v1"
				}
				if dir == "/" || dir == "." {
					break
				}
			}
		}
	}
	return version
}

// narrow lowers the machine's memory to a cgroup's limit and what it has left.
func narrow(r *Readings, limit, used uint64) {
	r.MemTotal = min(r.MemTotal, limit)
	left := uint64(0)
	if limit > used {
		left = limit - used
	}
	r.MemAvail = min(r.MemAvail, left)
}

func readNumber(file string) (uint64, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(data))
	if text == "max" {
		return unlimited, true
	}
	n, err := strconv.ParseUint(text, 10, 64)
	return n, err == nil
}

// Darwin reads macOS through sysctl and vm_stat. Run is a field so a test can
// answer with fixture output.
type Darwin struct {
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Read implements Source.
func (d Darwin) Read(ctx context.Context) (Readings, error) {
	r := Readings{Cores: runtime.NumCPU(), PressureCPU: -1, PressureMem: -1, Source: "macOS sysctl + vm_stat"}
	size, err := d.Run(ctx, "sysctl", "-n", "hw.memsize")
	if err != nil {
		return Readings{}, err
	}
	if r.MemTotal, err = strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64); err != nil {
		return Readings{}, fmt.Errorf("hw.memsize: %w", err)
	}
	vm, err := d.Run(ctx, "vm_stat")
	if err != nil {
		return Readings{}, err
	}
	if r.MemAvail, err = ParseVMStat(vm); err != nil {
		return Readings{}, err
	}
	r.MemAvail = min(r.MemAvail, r.MemTotal)
	load, err := d.Run(ctx, "sysctl", "-n", "vm.loadavg")
	if err != nil {
		return Readings{}, err
	}
	if r.Load1, err = ParseLoadAvg(string(load)); err != nil {
		return Readings{}, err
	}
	return r, nil
}

// ParseVMStat returns the bytes macOS can give a new process without paging:
// free, inactive, speculative and purgeable pages.
func ParseVMStat(data []byte) (uint64, error) {
	page := uint64(0)
	var pages uint64
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if i := strings.Index(line, "page size of "); i >= 0 {
				fields := strings.Fields(line[i+len("page size of "):])
				if len(fields) > 0 {
					page, _ = strconv.ParseUint(fields[0], 10, 64)
				}
			}
			continue
		}
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
			n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), "."), 10, 64)
			if err == nil {
				pages += n
			}
		}
	}
	if page == 0 {
		return 0, fmt.Errorf("vm_stat gave no page size")
	}
	return pages * page, scanner.Err()
}

// ParseLoadAvg returns the one-minute load from "{ 1.23 1.50 1.60 }" or
// "1.23 1.50 1.60".
func ParseLoadAvg(text string) (float64, error) {
	fields := strings.Fields(strings.Trim(strings.TrimSpace(text), "{}"))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty load average")
	}
	return strconv.ParseFloat(fields[0], 64)
}
