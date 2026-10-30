package doctor

import (
	"bufio"
	bytebuf "bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Readings is what the machine looks like now, as one source reports it.
// Memory is the smaller of the machine's and the container's: a cgroup limit
// counts, so containers, WSL and systemd slices are judged by what they can use.
type Readings struct {
	MemTotal uint64
	MemAvail uint64
	// PressureMem is the share of the last 10 seconds in which
	// some task stalled (Linux PSI), or -1 when the source has none.
	PressureMem float64
	// Source names what was read, for the status the owner sees.
	Source string
}

// Source reads the machine memory needed for relief.
type Source interface {
	Read(ctx context.Context) (Readings, error)
}

// Detect returns the source for this platform, or nil when there is none.
func Detect() Source {
	switch runtime.GOOS {
	case "linux":
		return Linux{ProcDir: "/proc"}
	case "darwin":
		return Darwin{Run: runCommand}
	}
	return nil
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
	r := Readings{PressureMem: -1}
	meminfo, err := os.ReadFile(filepath.Join(l.ProcDir, "meminfo"))
	if err != nil {
		return Readings{}, err
	}
	if r.MemTotal, r.MemAvail, err = ParseMeminfo(meminfo); err != nil {
		return Readings{}, err
	}
	r.Source = "linux /proc"
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
	scanner := bufio.NewScanner(bytebuf.NewReader(data))
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

// ParseMountinfo returns the cgroup v2 mounts and the cgroup v1 memory mounts,
// in file order. A container can show the hierarchy at more than one place.
func ParseMountinfo(data []byte) (v2, v1 []mount) {
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
			v2 = append(v2, m)
		case post[0] == "cgroup" && strings.Contains(","+post[2]+",", ",memory,"):
			v1 = append(v1, m)
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
// the mount, which may show only a subtree. The paths are compared as text, so
// a namespace root such as "/.." (a mount root outside this process's own
// cgroup namespace) maps like any other prefix. It reports false when the path
// lies outside what the mount shows.
func (m mount) cgroupDir(cgPath string) (string, bool) {
	cgPath, root := strings.TrimRight("/"+strings.TrimLeft(cgPath, "/"), "/"), strings.TrimRight("/"+strings.TrimLeft(m.Root, "/"), "/")
	var rel string
	switch {
	case root == "":
		rel = cgPath
	case cgPath == root:
		rel = ""
	case strings.HasPrefix(cgPath, root+"/"):
		rel = strings.TrimPrefix(cgPath, root)
	default:
		return "", false
	}
	// A path that climbs out of the mount ("/.." under a mount of the whole
	// hierarchy) names a cgroup the mount does not show.
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", false
		}
	}
	return filepath.Join(m.Point, rel), true
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
		var mounts []mount
		limitFile, usageFile, ver := "memory.max", "memory.current", "v2"
		switch {
		case parts[0] == "0" && parts[1] == "":
			mounts = v2
		case strings.Contains(","+parts[1]+",", ",memory,"):
			mounts, limitFile, usageFile, ver = v1, "memory.limit_in_bytes", "memory.usage_in_bytes", "v1"
		}
		// The process's own cgroup, then the agents' slice beside it: Dyna's agents
		// run in dyna.slice, a sibling of the engine's app.slice under the same
		// user manager, and it has a memory cap of its own.
		paths := []string{parts[2]}
		if agents := agentSlice(parts[2]); agents != "" {
			paths = append(paths, agents)
		}
		for _, cgPath := range paths {
			// The first mount that shows this cgroup is the one to read.
			var m mount
			dir, ok := "", false
			for _, candidate := range mounts {
				if dir, ok = candidate.cgroupDir(cgPath); ok {
					m = candidate
					break
				}
			}
			if !ok {
				if len(mounts) > 0 {
					// The process, or the agents' slice beside it, lives in a cgroup that
					// no mount shows, so its limit is unknown. The host's memory must not
					// stand in for it.
					return "", fmt.Errorf("cgroup %s is not visible in any cgroup mount, so its memory limit is unknown", cgPath)
				}
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
	}
	return version, nil
}

// agentSlice returns the cgroup path of dyna.slice beside the given path, under
// the same systemd user manager, or "" when the path is not under one.
func agentSlice(cgPath string) string {
	parts := strings.Split(strings.Trim(cgPath, "/"), "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "user@") && strings.HasSuffix(p, ".service") {
			return "/" + strings.Join(parts[:i+1], "/") + "/dyna.slice"
		}
	}
	return ""
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
	r := Readings{PressureMem: -1, Source: "macOS sysctl + vm_stat"}
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
	return r, nil
}

// ParseVMStat returns the bytes macOS can give a new process without paging:
// free, inactive, speculative and purgeable pages.
func ParseVMStat(data []byte) (uint64, error) {
	page := uint64(0)
	var pages uint64
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytebuf.NewReader(data))
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
