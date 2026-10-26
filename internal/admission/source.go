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

// Detect returns the source and the turn-memory sampler for this platform, or
// nil when there is none.
func Detect() (Source, Sampler) {
	switch runtime.GOOS {
	case "linux":
		return Linux{ProcDir: "/proc"}, ProcSampler{ProcDir: "/proc"}
	case "darwin":
		return Darwin{Run: runCommand}, PSSampler{Run: runCommand}
	}
	return nil, nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Linux reads /proc, PSI and the cgroup limits of the process's own cgroup,
// found through /proc/self/mountinfo. The directory is a field so a test can
// point it at fixture files.
type Linux struct{ ProcDir string }

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
	version, err := l.applyCgroup(&r)
	if err != nil {
		return Readings{}, err
	}
	if version != "" {
		r.Source += " + cgroup " + version
	}
	return r, nil
}

// ParseMeminfo returns MemTotal and MemAvailable in bytes. A kernel without
// MemAvailable gets free plus buffers plus cache.
func ParseMeminfo(data []byte) (total, avail uint64, err error) {
	var free, buffers, cached uint64
	haveAvail := false
	have := map[string]bool{}
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
		have[key] = true
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
		// An old kernel has no MemAvailable; free plus buffers plus cache stands
		// in, but only if all three are there. Zero available memory is never a
		// guess.
		if !have["MemFree"] || !have["Buffers"] || !have["Cached"] {
			return 0, 0, fmt.Errorf("meminfo has no MemAvailable and not all of MemFree, Buffers and Cached")
		}
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

// mount is one cgroup mount from /proc/self/mountinfo: what part of the host's
// hierarchy it shows (Root), where it is (Point), and for v1 its controllers.
type mount struct {
	Root, Point string
	V2          bool
}

// ParseMountinfo returns the cgroup v2 mount and the cgroup v1 memory mount.
func ParseMountinfo(data []byte) (v2, v1 *mount) {
	for _, line := range strings.Split(string(data), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		pre, post := strings.Fields(before), strings.Fields(after)
		if len(pre) < 5 || len(post) < 3 {
			continue
		}
		m := mount{Root: unescapeMount(pre[3]), Point: unescapeMount(pre[4])}
		switch {
		case post[0] == "cgroup2":
			m.V2 = true
			if v2 == nil {
				v2 = &m
			}
		case post[0] == "cgroup" && strings.Contains(","+post[2]+",", ",memory,"):
			if v1 == nil {
				v1 = &m
			}
		}
	}
	return v2, v1
}

// unescapeMount decodes the octal escapes mountinfo uses for spaces.
func unescapeMount(s string) string {
	r := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return r.Replace(s)
}

// cgroupDir maps a cgroup path of the host's hierarchy to its directory under
// the mount, which may show only a subtree. It reports false when the path lies
// outside what the mount shows.
func (m mount) cgroupDir(cgPath string) (string, bool) {
	cgPath, root := path.Clean("/"+cgPath), path.Clean("/"+m.Root)
	switch {
	case root == "/":
		return filepath.Join(m.Point, cgPath), true
	case cgPath == root:
		return m.Point, true
	case strings.HasPrefix(cgPath, root+"/"):
		return filepath.Join(m.Point, strings.TrimPrefix(cgPath, root)), true
	}
	return "", false
}

// applyCgroup narrows r to the memory limits of the process's cgroup and its
// parents, and returns the cgroup version it used, or "" if none limits it. A
// limit whose usage cannot be read is an error, because presenting the whole
// limit as free would overstate what is left.
func (l Linux) applyCgroup(r *Readings) (string, error) {
	mounts, err := os.ReadFile(filepath.Join(l.ProcDir, "self", "mountinfo"))
	if err != nil {
		return "", nil // no mountinfo: the host's memory stands
	}
	self, err := os.ReadFile(filepath.Join(l.ProcDir, "self", "cgroup"))
	if err != nil {
		return "", nil
	}
	v2, v1 := ParseMountinfo(mounts)
	version := ""
	for _, line := range strings.Split(string(self), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		var m *mount
		limitFile, usageFile, ver := "memory.max", "memory.current", "v2"
		switch {
		case parts[0] == "0" && parts[1] == "":
			m = v2
		case strings.Contains(","+parts[1]+",", ",memory,"):
			m, limitFile, usageFile, ver = v1, "memory.limit_in_bytes", "memory.usage_in_bytes", "v1"
		}
		if m == nil {
			continue
		}
		dir, ok := m.cgroupDir(parts[2])
		if !ok {
			continue
		}
		for {
			if limit, ok := readNumber(filepath.Join(dir, limitFile)); ok && limit < unlimited {
				used, ok := readNumber(filepath.Join(dir, usageFile))
				if !ok {
					return "", fmt.Errorf("cgroup %s has a memory limit but no readable usage", dir)
				}
				narrow(r, limit, used)
				version = ver
			}
			if dir == m.Point || dir == "/" || dir == "." {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return version, nil
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
	seen := map[string]bool{}
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
			if err != nil {
				return 0, fmt.Errorf("vm_stat: %q is not a page count", line)
			}
			pages += n
			seen[key] = true
		}
	}
	if page == 0 {
		return 0, fmt.Errorf("vm_stat gave no page size")
	}
	if !seen["Pages free"] || !seen["Pages inactive"] {
		return 0, fmt.Errorf("vm_stat gave no free or inactive page count")
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
