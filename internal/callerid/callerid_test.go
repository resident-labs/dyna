package callerid

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	user     = "/user.slice/user-1000.slice/user@1000.service"
	engineCG = user + "/app.slice/dyna-engine.service"
	turnUnit = "dyna-ses_20261020-130101-42bc169e-1a2b3c4d.scope"
	turnCG   = user + "/dyna.slice/" + turnUnit
	engine   = 100
)

type records struct {
	scopes    map[string]Identity
	processes map[Process]Identity
	err       error
}

func (r records) ScopeIdentity(unit string) (Identity, bool, error) {
	id, ok := r.scopes[unit]
	return id, ok, r.err
}

func (r records) ProcessIdentity(p Process) (Identity, bool, error) {
	id, ok := r.processes[p]
	return id, ok, r.err
}

// fakeProc writes /proc/<pid>/cgroup and stat lines for each process.
type fakeProc struct {
	t    *testing.T
	root string
}

func (f fakeProc) add(pid, parent int, start uint64, group string) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	// A cgroup v1 line comes first on hybrid systems; only "0::" counts.
	cgroup := "1:name=systemd:/ignored\n0::" + group + "\n"
	// The command name holds a space and a parenthesis, as real ones can.
	fields := make([]string, 0, 50)
	fields = append(fields, strconv.Itoa(pid), "(dyna (x) y)", "S", strconv.Itoa(parent))
	for len(fields) < 21 {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.FormatUint(start, 10), "0", "0")
	for name, data := range map[string]string{"cgroup": cgroup, "stat": strings.Join(fields, " ") + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

var testRecords = records{
	scopes:    map[string]Identity{turnUnit: {Kind: Session, Session: "ses_turn"}},
	processes: map[Process]Identity{{PID: 200, Start: 20}: {Kind: Session, Session: "ses_runner"}},
}

// newResolver starts the engine in group: its own unit, or a cgroup it
// shares with the owner's shell when no user systemd manager runs.
func newResolver(t *testing.T, group string) (Resolver, fakeProc) {
	t.Helper()
	proc := fakeProc{t, t.TempDir()}
	proc.add(engine, 1, 10, group)
	r, err := New(proc.root, engine, testRecords)
	if err != nil {
		t.Fatal(err)
	}
	return r, proc
}

func TestResolveRules(t *testing.T) {
	r, proc := newResolver(t, engineCG)
	// (a) a recorded scope is its session.
	proc.add(300, 1, 30, turnCG)
	// (b) an unrecorded scope under dyna.slice, for example one an agent made.
	proc.add(301, 1, 31, user+"/dyna.slice/dyna-made-up-00000000.scope")
	// (c) a child of the engine that runs without a scope (DYNA_SLICE=off).
	proc.add(302, engine, 32, engineCG)
	// (d) the owner's terminal and the desktop app.
	proc.add(303, 1, 33, user+"/app.slice/app-ghostty-1234.scope")
	proc.add(304, 1, 34, user+"/app.slice/dyna-desktop.service")
	for _, tc := range []struct {
		name string
		pid  int
		want Identity
	}{
		{"recorded scope", 300, Identity{Kind: Session, Session: "ses_turn"}},
		{"unrecorded agent scope", 301, Identity{Kind: Refused}},
		{"unrecorded child of the engine", 302, Identity{Kind: Refused}},
		{"owner terminal", 303, Identity{Kind: Owner}},
		{"desktop app", 304, Identity{Kind: Owner}},
		{"the engine itself", engine, Identity{Kind: Owner}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Resolve(tc.pid)
			if got.Kind != tc.want.Kind || got.Session != tc.want.Session || (got.Kind == Refused) != (got.Reason != "") {
				t.Fatalf("Resolve(%d) = %+v, want %+v", tc.pid, got, tc.want)
			}
		})
	}
}

// An agent's dyna command is a grandchild of the recorded _turn runner, so a
// recorded ancestor decides the session before any cgroup rule.
func TestRecordedAncestorDecidesSession(t *testing.T) {
	r, proc := newResolver(t, engineCG)
	proc.add(200, engine, 20, engineCG) // the recorded runner
	proc.add(201, 200, 21, engineCG)    // the harness
	proc.add(202, 201, 22, engineCG)    // the agent's dyna command
	if got := r.Resolve(202); got.Kind != Session || got.Session != "ses_runner" {
		t.Fatalf("grandchild of a recorded runner = %+v, want ses_runner", got)
	}
	// A session child that ended up outside dyna.slice keeps its parent
	// chain, so it is still the session, never the owner.
	proc.add(203, 202, 23, user+"/app.slice/app-ghostty-1234.scope")
	if got := r.Resolve(203); got.Kind != Session || got.Session != "ses_runner" {
		t.Fatalf("session grandchild in app.slice = %+v, want ses_runner", got)
	}
	// A detached child (reparented to PID 1) in the engine's own unit stays
	// refused.
	proc.add(204, 1, 24, engineCG)
	if got := r.Resolve(204); got.Kind != Refused {
		t.Fatalf("detached child in the engine's unit = %+v, want refused", got)
	}
	// The same PID with another start time is a new process: refused.
	proc.add(200, engine, 99, engineCG)
	if got := r.Resolve(202); got.Kind != Refused {
		t.Fatalf("grandchild of a reused PID = %+v, want refused", got)
	}
}

// Without a user systemd manager (containers, some WSL setups) the engine,
// the owner's shell and everything else share one cgroup. Only the engine's
// unrecorded descendants are refused there.
func TestSharedCgroupKeepsOwner(t *testing.T) {
	shared := "/init.scope"
	r, proc := newResolver(t, shared)
	proc.add(600, 1, 60, shared)      // the owner's shell
	proc.add(601, 600, 61, shared)    // a dyna command in it
	proc.add(602, engine, 62, shared) // an unrecorded child of the engine
	proc.add(200, engine, 20, shared) // the recorded runner
	proc.add(603, 200, 63, shared)    // the agent's dyna command
	for pid, want := range map[int]Identity{
		601: {Kind: Owner},
		602: {Kind: Refused},
		603: {Kind: Session, Session: "ses_runner"},
	} {
		if got := r.Resolve(pid); got.Kind != want.Kind || got.Session != want.Session {
			t.Fatalf("Resolve(%d) in a shared cgroup = %+v, want %+v", pid, got, want)
		}
	}
}

func TestUnreadableCallerIsRefused(t *testing.T) {
	r, proc := newResolver(t, engineCG)
	if got := r.Resolve(999); got.Kind != Refused || !strings.Contains(got.Reason, "cgroup") {
		t.Fatalf("a caller without /proc entries = %+v, want refused", got)
	}
	// A broken ancestor fails closed too.
	proc.add(400, 401, 40, engineCG)
	if got := r.Resolve(400); got.Kind != Refused {
		t.Fatalf("a caller with an unreadable parent = %+v, want refused", got)
	}
	if _, err := New(proc.root, 998, testRecords); err == nil {
		t.Fatal("New with an unreadable engine cgroup succeeded")
	}
	// A failed record lookup never makes an owner.
	proc.add(403, 1, 43, user+"/app.slice/app-ghostty-1234.scope")
	broken, err := New(proc.root, engine, records{err: errors.New("database is locked")})
	if err != nil {
		t.Fatal(err)
	}
	if got := broken.Resolve(403); got.Kind != Refused || !strings.Contains(got.Reason, "launch records") {
		t.Fatalf("a caller during a record lookup error = %+v, want refused", got)
	}
}

func TestReadStatReadsStartTime(t *testing.T) {
	_, proc := newResolver(t, engineCG)
	proc.add(500, 1, 12345, engineCG)
	if got, parent, err := readStat(proc.root, 500); err != nil || got != (Process{PID: 500, Start: 12345}) || parent != 1 {
		t.Fatalf("readStat(500) = %+v, parent %d, %v", got, parent, err)
	}
	// The real /proc parses too, so the field count matches the kernel.
	self, _, err := readStat("/proc", os.Getpid())
	if err != nil || self.Start == 0 {
		t.Fatalf("own process: %+v, %v", self, err)
	}
}

// The engine reads its own cgroup to tell whether it is the live engine in
// its dyna-engine.service unit.
func TestCgroupNamesTheEngineUnit(t *testing.T) {
	proc := fakeProc{t, t.TempDir()}
	proc.add(7, 1, 1, "/user.slice/user-1000.slice/user@1000.service/app.slice/dyna-engine.service")
	proc.add(8, 1, 1, "/user.slice/user-1000.slice/user@1000.service/app.slice/app-term.scope")
	for pid, want := range map[int]bool{7: true, 8: false} {
		group, err := Cgroup(proc.root, pid)
		if err != nil || EngineUnit(group) != want {
			t.Fatalf("pid %d: Cgroup = %q, %v; EngineUnit = %v, want %v", pid, group, err, EngineUnit(group), want)
		}
	}
	if _, err := Cgroup(proc.root, 9); err == nil {
		t.Fatal("Cgroup read a process that does not exist")
	}
}

// While the engine is down its PID is unknown, so rule (c) cannot run. A
// launcher that would only be the owner by default is refused then; a
// recorded session still resolves.
func TestLauncherWithoutEngineIsNeverAnUnconfirmedOwner(t *testing.T) {
	_, proc := newResolver(t, engineCG)
	proc.add(700, engine, 70, engineCG)                            // unrecorded, in the engine's cgroup
	proc.add(701, 1, 71, user+"/app.slice/app-ghostty-1234.scope") // the owner's terminal
	proc.add(200, engine, 20, engineCG)                            // the recorded runner
	proc.add(702, 200, 72, engineCG)                               // its agent's dyna command
	for pid, want := range map[int]Kind{700: Refused, 701: Refused, 702: Session} {
		got, err := resolveLauncher(proc.root, 0, testRecords, pid)
		if got.Kind != want || (err == nil) != (want != Refused) {
			t.Fatalf("launcher %d with no engine = %+v (%v), want %s", pid, got, err, want)
		}
	}
	// With the engine answering, the owner's terminal is the owner.
	if got, err := resolveLauncher(proc.root, engine, testRecords, 701); err != nil || got.Kind != Owner {
		t.Fatalf("owner terminal with the engine = %+v (%v), want owner", got, err)
	}
}

// The owner-side callers of the socket and the rule each one meets
// (perf-lead's list for part 3). The computers unit is refused instead, see
// TestComputersUnitIsRefused.
func TestOwnerSideCallersAreTheOwner(t *testing.T) {
	r, proc := newResolver(t, engineCG)
	for pid, group := range map[int]string{
		900: user + "/app.slice/dyna-desktop.service",           // (d) the desktop app
		901: user + "/app.slice/app-ghostty-1234.scope",         // (d) the owner's terminal, and git hooks run from it
		902: user + "/app.slice/dyna-install-1a2b.service",      // (d) the install worker's transient unit
		903: "/user.slice/user-1000.slice/session-3.scope",      // (d) an external orchestrator outside any scope (item 39(a))
		904: user + "/app.slice/app-org.gnome.Terminal.slice/x", // (d) another owner app
	} {
		proc.add(pid, 1, uint64(pid), group)
		if got := r.Resolve(pid); got.Kind != Owner {
			t.Fatalf("owner-side caller in %s = %+v, want the owner", group, got)
		}
	}
}
