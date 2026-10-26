// Package callerid decides who calls the engine from the calling process
// itself: its cgroup and its ancestors. No token and no environment variable
// decides identity, because every agent can set them (requirements item 3).
//
// Agents run as the owner's Unix user, so this stops mistakes, not a hostile
// agent: a command started with systemd-run --user --scope outside dyna.slice
// is the owner. A caller PID from SO_PEERCRED can also be reused between
// connect and the /proc read; SO_PEERPIDFD (Linux 6.5) removes that race.
// Without a user systemd manager, a child that detaches from the engine is
// reparented away from it, and in a cgroup it shares with the owner's shell
// it then counts as the owner. The engine is not a subreaper: reaping
// adopted orphans would steal exits from its own exec.Cmd.Wait calls. An
// owner record is permitted only through the engine-authorized hook and
// terminal launch paths. Identity needs Linux /proc and cgroups: the engine
// runs only on Linux and WSL, and elsewhere every caller is refused.
package callerid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/agentslice"
	"dyna-agent/internal/procident"
)

type Kind string

const (
	Owner   Kind = "owner"
	Session Kind = "session"
	// Worker is a workflow worker: authority's worker role, never a session.
	Worker  Kind = "worker"
	Refused Kind = "refused"
)

// Identity is who a caller acts for: a session, a workflow run's worker, or
// the owner. Reason explains a refusal.
type Identity struct {
	Kind    Kind
	Session string // for Session
	Run     string // for Worker
	Agent   int    // for Worker: always 0, the run itself; a workflow agent is a session
	Reason  string
}

// Process names one process for its whole life. Start is the start time from
// /proc/<pid>/stat, so a reused PID never matches an older record.
type Process struct {
	PID   int
	Start uint64
}

// Records are the scopes and processes launched for an identity. A lookup
// error refuses the caller, so a failed read never makes an owner.
type Records interface {
	ScopeIdentity(unit string) (Identity, bool, error)
	ProcessIdentity(p Process) (Identity, bool, error)
}

// Resolver applies the identity rules to a caller's PID.
type Resolver struct {
	proc    string
	engine  int
	group   string // the engine's cgroup, which does not change while it runs
	ownUnit bool   // the engine runs in its own dyna-engine.service unit
	records Records
}

// New reads the engine's cgroup once. proc is "/proc" outside tests. An
// engine PID of 0 means no engine runs, so no caller shares its cgroup and
// rule (c) cannot apply.
func New(proc string, engine int, records Records) (Resolver, error) {
	r := Resolver{proc: proc, engine: engine, records: records}
	if engine <= 0 {
		return r, nil
	}
	group, err := r.cgroup(engine)
	if err != nil {
		return Resolver{}, fmt.Errorf("read the engine's cgroup: %w", err)
	}
	r.group, r.ownUnit = group, EngineUnit(group)
	return r, nil
}

// EngineUnit reports whether a cgroup is the engine's own
// dyna-engine.service unit, as the live engine's is.
func EngineUnit(group string) bool { return filepath.Base(group) == agentslice.EngineUnit }

// Resolve applies the rules in order and fails closed when the caller's
// process cannot be read. First, a caller that was recorded at its launch,
// or whose ancestor was, has that record's identity whatever its cgroup; an
// agent's dyna command is a grandchild of its recorded runner. Then:
//
//	(a) a caller in a recorded scope has that scope's identity;
//	(b) any other caller under dyna.slice is refused;
//	(c) any other caller in the engine's cgroup except the engine is refused
//	    when it descends from the engine, or when the engine has its own
//	    unit. Without a user systemd manager the owner's shell shares the
//	    engine's cgroup, and only the engine's descendants are its children;
//	(d) any other caller is the owner.
func (r Resolver) Resolve(pid int) Identity {
	group, err := r.cgroup(pid)
	if err != nil {
		return refused("cannot read the caller's cgroup: %v", err)
	}
	p := pid
	for p > 1 && p != r.engine {
		process, parent, err := readStat(r.proc, p)
		if err != nil {
			return refused("cannot read the caller's process %d (is /proc mounted with hidepid?): %v", p, err)
		}
		id, ok, err := r.records.ProcessIdentity(process)
		if err != nil {
			return refused("cannot read the launch records: %v", err)
		}
		if ok {
			return id
		}
		p = parent
	}
	sliceUnit := "" // the unit right under dyna.slice, if the caller is there
	units := strings.Split(group, "/")
	for i, unit := range units {
		if unit == agentslice.Slice {
			sliceUnit = unit
			if i+1 < len(units) {
				sliceUnit = units[i+1]
			}
		}
		if !strings.HasSuffix(unit, ".scope") {
			continue
		}
		id, ok, err := r.records.ScopeIdentity(unit)
		if err != nil {
			return refused("cannot read the launch records: %v", err)
		}
		if ok {
			return id
		}
	}
	if sliceUnit != "" {
		return refused("rule (b): the caller runs in %s, under %s, which Dyna did not record for a session", sliceUnit, agentslice.Slice)
	}
	if r.engine > 0 && group == r.group && pid != r.engine && (p == r.engine || r.ownUnit) {
		return refused("rule (c): the caller runs in the engine's cgroup, but Dyna did not start it for a session")
	}
	return Identity{Kind: Owner}
}

func refused(format string, args ...any) Identity {
	return Identity{Kind: Refused, Reason: fmt.Sprintf(format, args...)}
}

// cgroup returns the unified (v2) cgroup path of pid.
func (r Resolver) cgroup(pid int) (string, error) { return Cgroup(r.proc, pid) }

// Cgroup returns the unified (v2) cgroup path of pid. proc is "/proc"
// outside tests.
func Cgroup(proc string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if group, ok := strings.CutPrefix(line, "0::"); ok && group != "" {
			return group, nil
		}
	}
	return "", errors.New("no unified cgroup line")
}

// ProcessOf names the live process pid with its start time, so a later
// check can tell it apart from a process that reuses its PID.
func ProcessOf(pid int) (Process, error) {
	p, _, err := readStat("/proc", pid)
	return p, err
}

// readStat reads pid's start time and parent from /proc/<pid>/stat.
func readStat(proc string, pid int) (Process, int, error) {
	stat, err := procident.Read(proc, pid)
	if err != nil {
		return Process{}, 0, err
	}
	return Process{PID: stat.PID, Start: stat.StartTime}, stat.ParentPID, nil
}
