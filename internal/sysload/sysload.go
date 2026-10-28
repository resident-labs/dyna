// Package sysload samples machine load and Dyna's share of it from /proc and
// the systemd user cgroup tree. It reads only a few fixed files and never
// scans /proc/<pid>.
package sysload

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Interval is the time between samples.
const Interval = 5 * time.Second

// MaxTop is the most sessions that Load.Top lists.
const MaxTop = 3

// userHZ is the unit of the times in /proc/stat. It is 100 on every Linux
// build that Dyna supports.
const userHZ = 100

// Load is one reading. CPUPercent and Split are nil until two samples exist.
// Split values are percent of the whole machine. Memory values are bytes.
type Load struct {
	At         time.Time  `json:"at"`
	CPUs       int        `json:"cpus"`
	Load       [3]float64 `json:"load"`
	CPUPercent *float64   `json:"cpuPercent"`
	Split      *Split     `json:"split"`
	Memory     Memory     `json:"memory"`
	Top        []Top      `json:"top"`
	DynaKnown  bool       `json:"dynaKnown"`
	// Groups feed the doctor's stored samples; the web app does not read them.
	Groups []Group `json:"-"`
}

// Group is the use of one group of cgroups over the last interval. Groups
// are machine, engine, agents, computers and session:<id>. They need two
// readings; only machine exists without the cgroup tree.
type Group struct {
	Name     string
	Cores    float64
	Memory   uint64  // bytes without the inactive file cache
	IO       float64 // bytes read and written per second; 0 without the io controller
	Pressure Pressure
}

// Pressure is the "some" avg10 of each PSI file: the percent of the last
// 10 s in which at least one task stalled.
type Pressure struct{ CPU, Memory, IO float64 }

// Split divides the busy CPU time of the machine.
type Split struct {
	Engine    float64 `json:"engine"`
	Agents    float64 `json:"agents"`
	Computers float64 `json:"computers"`
	Other     float64 `json:"other"`
}

// Memory holds machine memory in bytes. Dyna is zero when DynaKnown is false.
type Memory struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Dyna  uint64 `json:"dyna"`
}

// Top is one session (or run) and the cores it used in the last interval.
type Top struct {
	ID    string  `json:"id"`
	Cores float64 `json:"cores"`
}

// reading is the raw counters of one sample. CPU times are in microseconds.
type reading struct {
	at       time.Time
	cpus     int  // online CPUs of the machine, from /proc/stat
	known    bool // the cgroup counters below were read
	busy     uint64
	pressure Pressure            // the machine's, from /proc/pressure
	groups   map[string]counters // engine, dyna, computers, and scope file names
}

// counters is one group's reading. cpu and io are counters; memory and
// pressure are gauges.
type counters struct {
	cpu, io, memory uint64
	pressure        Pressure
}

// RecordEvery is the time between two readings that Record receives.
const RecordEvery = time.Minute

// Sampler keeps the last two readings behind a mutex.
type Sampler struct {
	procRoot, cgroupRoot string
	logf                 func(format string, args ...any)
	lastErr              string // the failure last logged; only Run reads it
	// Record, when set, gets a Load averaged over about RecordEvery, once per
	// RecordEvery, on the sampling goroutine.
	Record func(Load)

	mu     sync.Mutex
	prev   *reading
	minute *minute // what the next Record averages; nil until the first sample
	last   Load
}

// minute adds up each interval's groups until Record gets them. Adding every
// interval keeps the work of a session whose scope ended inside the minute.
type minute struct {
	from   time.Time
	groups []Group // Cores holds core-seconds and IO holds bytes until load
}

// add adds groups measured over secs. Memory and pressure are gauges, so the
// newest reading wins; a group that left keeps its last one.
func (m *minute) add(groups []Group, secs float64) {
next:
	for _, g := range groups {
		g.Cores, g.IO = g.Cores*secs, g.IO*secs
		for i := range m.groups {
			if m.groups[i].Name == g.Name {
				m.groups[i].Cores += g.Cores
				m.groups[i].IO += g.IO
				m.groups[i].Memory, m.groups[i].Pressure = g.Memory, g.Pressure
				continue next
			}
		}
		m.groups = append(m.groups, g)
	}
}

// load returns the minute's averages with the machine values of out.
func (m *minute) load(out Load) *Load {
	secs := out.At.Sub(m.from).Seconds()
	for i := range m.groups {
		m.groups[i].Cores /= secs
		m.groups[i].IO /= secs
	}
	return &Load{At: out.At, CPUs: out.CPUs, Load: out.Load, Memory: out.Memory, DynaKnown: out.DynaKnown, Groups: m.groups}
}

// New returns a sampler of the real /proc and /sys/fs/cgroup.
func New() *Sampler {
	return &Sampler{procRoot: "/proc", cgroupRoot: "/sys/fs/cgroup", logf: log.Printf}
}

// Load returns the newest sample.
func (s *Sampler) Load() Load {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Run samples now and then every Interval until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	tick := time.NewTicker(Interval)
	defer tick.Stop()
	for {
		s.tick(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// tick samples and logs a failure once until it changes or clears. A host
// without /proc would otherwise log every interval.
func (s *Sampler) tick(now time.Time) {
	msg := ""
	if err := s.sample(now); err != nil {
		msg = err.Error()
	}
	if msg != "" && msg != s.lastErr {
		s.logf("sysload: %s", msg)
	}
	s.lastErr = msg
}

func (s *Sampler) sample(now time.Time) error {
	out := Load{At: now}
	var err error
	if out.Load, err = s.loadavg(); err != nil {
		return err
	}
	if out.Memory.Total, out.Memory.Used, err = s.meminfo(); err != nil {
		return err
	}
	cur := &reading{at: now, pressure: pressure(filepath.Join(s.procRoot, "pressure"), "")}
	if cur.busy, cur.cpus, err = s.cpuStat(); err != nil {
		return err
	}
	out.CPUs = cur.cpus
	if root, ok := s.userRoot(); ok {
		cur.known = s.readCgroups(root, cur, &out)
	}
	s.mu.Lock()
	if prev := s.prev; prev != nil && now.After(prev.at) {
		diff(prev, cur, &out)
	}
	var record *Load
	if s.Record != nil {
		if s.minute == nil {
			s.minute = &minute{from: now}
		} else if s.prev != nil {
			s.minute.add(out.Groups, now.Sub(s.prev.at).Seconds())
			if now.Sub(s.minute.from) >= RecordEvery {
				record = s.minute.load(out)
				s.minute = &minute{from: now}
			}
		}
	}
	s.prev, s.last = cur, out
	s.mu.Unlock()
	if record != nil {
		s.Record(*record)
	}
	return nil
}

// diff fills the CPU fields and groups of out from two readings.
func diff(prev, cur *reading, out *Load) {
	secs := cur.at.Sub(prev.at).Seconds()
	cores := func(before, after uint64) float64 { return float64(sub(after, before)) / 1e6 / secs }
	rate := func(before, after uint64) float64 { return float64(sub(after, before)) / secs }
	pct := func(c float64) float64 { return c / float64(cur.cpus) * 100 }
	busy := cores(prev.busy, cur.busy)
	total := pct(busy)
	out.CPUPercent = &total
	out.Groups = []Group{{Name: "machine", Cores: busy, Memory: out.Memory.Used, Pressure: cur.pressure}}
	// Counters from a missing tree are zero: the first reading after it
	// appears would show its lifetime usage as one interval.
	if !prev.known || !cur.known {
		return
	}
	group := func(name, key string) Group {
		b, a := prev.groups[key], cur.groups[key]
		return Group{Name: name, Cores: cores(b.cpu, a.cpu), Memory: a.memory, IO: rate(b.io, a.io), Pressure: a.pressure}
	}
	engine, computers, agents := group("engine", "engine"), group("computers", "computers"), group("agents", "dyna")
	// A room computer's scope is counted on its own and added to the computers.
	for key := range cur.groups {
		if strings.HasPrefix(key, roomComputerKey) {
			g := group("", key)
			computers.Cores += g.Cores
			computers.Memory += g.Memory
			computers.IO += g.IO
			computers.Pressure = maxPressure(computers.Pressure, g.Pressure)
		}
	}
	// Computers run inside dyna.slice, so take them out of the agents.
	agents.Cores = max(0, agents.Cores-computers.Cores)
	agents.Memory = sub(agents.Memory, computers.Memory)
	agents.IO = max(0, agents.IO-computers.IO)
	out.Groups = append(out.Groups, engine, agents, computers)
	split := Split{Engine: pct(engine.Cores), Computers: pct(computers.Cores), Agents: pct(agents.Cores)}
	split.Other = max(0, total-split.Engine-split.Agents-split.Computers)
	out.Split = &split
	byID := map[string]Group{}
	for key := range cur.groups {
		// A room computer's scope is a computer, not an agent session.
		if !strings.HasSuffix(key, ".scope") || strings.HasPrefix(key, roomComputerKey) {
			continue
		}
		// A scope that is new counts as 0 before; one that left is not in cur.
		id := scopeID(key)
		g, sum := group("session:"+id, key), byID[id]
		sum.Name, sum.Cores, sum.Memory, sum.IO = g.Name, sum.Cores+g.Cores, sum.Memory+g.Memory, sum.IO+g.IO
		sum.Pressure = maxPressure(sum.Pressure, g.Pressure)
		byID[id] = sum
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g := byID[id]
		out.Groups = append(out.Groups, g)
		if g.Cores > 0 {
			out.Top = append(out.Top, Top{ID: id, Cores: g.Cores})
		}
	}
	sort.Slice(out.Top, func(i, j int) bool {
		if out.Top[i].Cores != out.Top[j].Cores {
			return out.Top[i].Cores > out.Top[j].Cores
		}
		return out.Top[i].ID < out.Top[j].ID
	})
	if len(out.Top) > MaxTop {
		out.Top = out.Top[:MaxTop]
	}
}

// sub returns a-b, or 0 when a counter went backwards.
func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func (s *Sampler) loadavg() (l [3]float64, err error) {
	b, err := os.ReadFile(filepath.Join(s.procRoot, "loadavg"))
	if err != nil {
		return l, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return l, errors.New("short loadavg")
	}
	for i := range l {
		if l[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return l, err
		}
	}
	return l, nil
}

// cpuStat reads /proc/stat. busy sums every field of the cpu line except
// idle and iowait, in microseconds; guest time is already part of user and
// nice, so the last two fields are skipped. cpus counts the cpuN lines. The
// line covers the whole machine, so runtime.NumCPU, which follows this
// process's affinity, is only the fallback.
func (s *Sampler) cpuStat() (busy uint64, cpus int, err error) {
	b, err := os.ReadFile(filepath.Join(s.procRoot, "stat"))
	if err != nil {
		return 0, 0, err
	}
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 0 || !strings.HasPrefix(f[0], "cpu"):
		case f[0] != "cpu":
			cpus++
		case len(f) >= 8:
			found = true
			for i, v := range f[1:] {
				if i == 3 || i == 4 || i > 7 { // idle, iowait, guest, guest_nice
					continue
				}
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return 0, 0, err
				}
				busy += n
			}
		}
	}
	if !found {
		return 0, 0, errors.New("no cpu line in stat")
	}
	if cpus == 0 {
		cpus = runtime.NumCPU()
	}
	return busy * (1e6 / userHZ), cpus, nil
}

func (s *Sampler) meminfo() (total, used uint64, err error) {
	b, err := os.ReadFile(filepath.Join(s.procRoot, "meminfo"))
	if err != nil {
		return 0, 0, err
	}
	var avail uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	if total == 0 {
		return 0, 0, errors.New("no MemTotal in meminfo")
	}
	return total, sub(total, avail), nil
}

var userManager = regexp.MustCompile(`^user@\d+\.service$`)

// userRoot finds the cgroup directory of the user manager that holds this
// process, and reports whether it holds dyna.slice.
func (s *Sampler) userRoot() (string, bool) {
	b, err := os.ReadFile(filepath.Join(s.procRoot, "self", "cgroup"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		path := line[strings.LastIndex(line, ":")+1:]
		parts := strings.Split(path, "/")
		for i, p := range parts {
			if userManager.MatchString(p) {
				root := filepath.Join(s.cgroupRoot, filepath.Join(parts[:i+1]...))
				_, err := os.Stat(filepath.Join(root, "dyna.slice"))
				return root, err == nil
			}
		}
	}
	return "", false
}

// readCgroups fills the cgroup counters of cur and the memory of out. It
// reports whether every CPU counter that exists was read: a failed read must
// not count as zero usage, or the next sample would show lifetime usage as one
// interval. A service that is not running has no cgroup and counts as zero.
func (s *Sampler) readCgroups(root string, cur *reading, out *Load) bool {
	dyna := filepath.Join(root, "dyna.slice")
	dirs := map[string][]string{
		"dyna":      {dyna},
		"computers": {filepath.Join(dyna, "dyna-computers.service")},
		"engine":    {filepath.Join(root, "app.slice", "dyna-engine.service"), filepath.Join(root, "app.slice", "dyna-serve.service")},
	}
	// A room computer runs in its own scope below its room's slice, outside
	// the service cgroup. Each scope keeps its own counters, so one that exits
	// cannot pull the computers total backwards.
	roomComputers, _ := filepath.Glob(filepath.Join(dyna, "dyna-room.slice", "dyna-room-*.slice", "dyna-computer-*.scope"))
	for _, p := range roomComputers {
		dirs[roomComputerKey+filepath.Base(p)] = []string{p}
	}
	names, _ := filepath.Glob(filepath.Join(dyna, "dyna-*.scope"))
	for _, p := range names {
		dirs[filepath.Base(p)] = []string{p}
	}
	out.DynaKnown = true
	ok := true
	cur.groups = map[string]counters{}
groups:
	for key, list := range dirs {
		var c counters
		for _, dir := range list {
			n, err := usage(dir)
			if errors.Is(err, fs.ErrNotExist) && (strings.HasSuffix(key, ".scope") || strings.HasPrefix(key, roomComputerKey)) {
				// The scope ended after the glob, so it has left.
				continue groups
			}
			if err != nil && (key == "dyna" || !errors.Is(err, fs.ErrNotExist)) {
				ok = false
			}
			c.cpu += n
			c.io += ioBytes(dir)
			c.memory += memory(dir)
			c.pressure = maxPressure(c.pressure, pressure(dir, ".pressure"))
		}
		cur.groups[key] = c
	}
	out.Memory.Dyna = cur.groups["dyna"].memory + cur.groups["engine"].memory
	return ok
}

// roomComputerKey prefixes the group key of one room computer's scope.
const roomComputerKey = "computer:"

// usage reads usage_usec from a cgroup's cpu.stat.
func usage(dir string) (uint64, error) {
	return statValue(filepath.Join(dir, "cpu.stat"), "usage_usec")
}

// ioBytes sums rbytes and wbytes over the devices in a cgroup's io.stat. It
// is 0 when the io controller is not enabled for the cgroup.
func ioBytes(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "io.stat"))
	if err != nil {
		return 0
	}
	var n uint64
	for _, field := range strings.Fields(string(b)) {
		key, value, _ := strings.Cut(field, "=")
		if key == "rbytes" || key == "wbytes" {
			v, _ := strconv.ParseUint(value, 10, 64)
			n += v
		}
	}
	return n
}

// pressure reads the "some avg10" of the cpu, memory and io PSI files in
// dir, named <resource><suffix>: /proc/pressure/cpu or a cgroup's
// cpu.pressure. A missing file reads as 0.
func pressure(dir, suffix string) Pressure {
	read := func(resource string) float64 {
		b, err := os.ReadFile(filepath.Join(dir, resource+suffix))
		if err != nil {
			return 0
		}
		line, _, _ := strings.Cut(string(b), "\n")
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "some" {
			return 0
		}
		v, _ := strconv.ParseFloat(strings.TrimPrefix(f[1], "avg10="), 64)
		return v
	}
	return Pressure{CPU: read("cpu"), Memory: read("memory"), IO: read("io")}
}

func maxPressure(a, b Pressure) Pressure {
	return Pressure{CPU: max(a.CPU, b.CPU), Memory: max(a.Memory, b.Memory), IO: max(a.IO, b.IO)}
}

// memory is memory.current without the inactive file cache.
func memory(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	inactive, _ := statValue(filepath.Join(dir, "memory.stat"), "inactive_file")
	return sub(n, inactive)
}

// statValue reads one key of a flat keyed file. A missing key is an error.
func statValue(path, key string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == key {
			return strconv.ParseUint(f[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("%s: no %s", path, key)
}

var scopeName = regexp.MustCompile(`^dyna-(.+)-[0-9a-f]{8}\.scope$`)

// scopeID returns the session or run ID in a scope name made by
// agentslice.unitName, or the file name when it does not match.
func scopeID(name string) string {
	if m := scopeName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return strings.TrimSuffix(name, ".scope")
}
