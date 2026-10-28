package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/proc"
)

// OutsideGroup is the name of the sample group that holds the machine's load
// beyond Dyna's engine, computers and sessions.
const OutsideGroup = "outside Dyna"

// clockTicks is the unit of the CPU times in /proc/<pid>/stat. Linux reports
// them in 1/100 s on every platform Dyna runs on.
const clockTicks = 100

// Process is a program on the machine that Dyna does not run, grouped by the
// unit it runs in and its command.
type Process struct {
	Command string
	// RoomID is the canonical room identity decoded from the process cgroup.
	// It is empty when no canonical room slice owns the process.
	RoomID agentslice.RoomID
	// Unit is the systemd unit (a scope or service) that holds the processes,
	// such as "horizon-swarm-3.scope". It is empty when the cgroup names none.
	Unit string
	// Cwd is the working folder of the group's first process.
	Cwd string
	// Count is how many processes the group holds.
	Count int
	// Cores is the CPU the group used while it was watched.
	Cores float64
	// Memory is the resident memory of the group in bytes.
	Memory uint64
}

type procReading struct {
	proc.Process
	roomID   agentslice.RoomID
	unit     string
	cwd      string
	outsider bool
}

// dynaCgroups name the places Dyna's own processes run in.
var dynaCgroups = []string{"/dyna.slice", "/dyna-engine.service", "/dyna-serve.service", "/dyna-computers.service"}

func readProcesses(procRoot string) map[int]procReading {
	out := map[int]procReading{}
	for pid, p := range proc.List(procRoot) {
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		r := procReading{Process: p, outsider: true}
		if cg, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
			contents := string(cg)
			for _, name := range dynaCgroups {
				if strings.Contains(contents, name) {
					r.outsider = false
				}
			}
			cgroup := ""
			for _, line := range strings.Split(contents, "\n") {
				if group, ok := strings.CutPrefix(line, "0::"); ok && group != "" {
					cgroup = group
					break
				}
			}
			r.roomID, _ = agentslice.RoomIDFromCgroup(cgroup)
			r.unit = cgroupUnit(contents)
			// Dyna launches agent work in scopes. A service in a room slice is a
			// detached program that room launched, so keep its canonical owner.
			if r.roomID != "" && strings.HasSuffix(r.unit, ".service") {
				r.outsider = true
			}
		}
		if cwd, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
			r.cwd = cwd
		}
		out[pid] = r
	}
	return out
}

// cgroupUnit returns the deepest scope or service in a /proc/<pid>/cgroup file.
func cgroupUnit(cgroup string) string {
	unit := ""
	for _, line := range strings.Split(cgroup, "\n") {
		for _, part := range strings.Split(line[strings.LastIndex(line, ":")+1:], "/") {
			if strings.HasSuffix(part, ".scope") || strings.HasSuffix(part, ".service") {
				unit = part
			}
		}
	}
	return unit
}

// TopOutside watches the processes under procRoot for interval, using sleep to
// wait, and returns up to n groups outside Dyna that used the most CPU. When
// none used CPU, it returns those that hold the most memory. Processes that
// share a unit and a command are one group.
func TopOutside(procRoot string, interval time.Duration, sleep func(time.Duration), n int) []Process {
	before := readProcesses(procRoot)
	sleep(interval)
	after := readProcesses(procRoot)
	type key struct {
		unit, command string
		roomID        agentslice.RoomID
	}
	groups := map[key]*Process{}
	var order []key
	for pid, cur := range after {
		if !cur.outsider {
			continue
		}
		k := key{cur.unit, cur.Command, cur.roomID}
		if cur.unit == "" {
			k.command = cur.Command + "\x00" + cur.cwd
		}
		g := groups[k]
		if g == nil {
			g = &Process{Command: cur.Command, RoomID: cur.roomID, Unit: cur.unit, Cwd: cur.cwd}
			groups[k] = g
			order = append(order, k)
		}
		g.Count++
		g.Memory += cur.Memory
		if prev, ok := before[pid]; ok && cur.CPUTicks >= prev.CPUTicks && interval > 0 {
			g.Cores += float64(cur.CPUTicks-prev.CPUTicks) / clockTicks / interval.Seconds()
		}
	}
	list := make([]Process, 0, len(order))
	busy := false
	for _, k := range order {
		list = append(list, *groups[k])
		busy = busy || groups[k].Cores >= 0.05
	}
	sort.SliceStable(list, func(i, j int) bool {
		if busy {
			return list[i].Cores > list[j].Cores
		}
		return list[i].Memory > list[j].Memory
	})
	if busy {
		for len(list) > 0 && list[len(list)-1].Cores < 0.05 {
			list = list[:len(list)-1]
		}
	}
	return list[:min(len(list), n)]
}

var sessionInPath = regexp.MustCompile(`ses_\d{8}-\d{6}-[0-9a-f]{8}`)

// SessionIn returns the session ID a working folder names, such as a Dyna
// worktree made for a session, or "" when it names none.
func SessionIn(cwd string) string { return sessionInPath.FindString(cwd) }

// Text is the group as one line a person can read.
func (p Process) Text(room string) string {
	s := p.Command
	if p.Count > 1 {
		s += fmt.Sprintf(" (%d processes)", p.Count)
	}
	s += fmt.Sprintf(" uses %.1f cores and %s", p.Cores, bytes(p.Memory))
	if p.Unit != "" {
		s += ", in " + p.Unit
	}
	if room != "" {
		s += ", room " + room
	} else if p.Cwd != "" {
		s += ", in " + p.Cwd
	}
	return s
}
