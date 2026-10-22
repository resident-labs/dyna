package callerid

import (
	"context"
	"dyna-agent/internal/authority"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

// startChild starts a shell that waits on its stdin, so it lives until the
// test closes the pipe.
func startChild(t *testing.T, l Launch) (*exec.Cmd, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "read line")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	err = Start(context.Background(), cmd, l)
	if err == nil {
		t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
	}
	return cmd, err
}

func TestStartRecordsTheSessionProcess(t *testing.T) {
	dir := t.TempDir()
	cmd, err := startChild(t, Launch{DataDir: dir, Name: "ses_a", Actor: Identity{Kind: Session, Session: "ses_a"}, Launcher: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := readStat("/proc", cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok, err := s.ProcessIdentity(p); err != nil || !ok || id != (Identity{Kind: Session, Session: "ses_a"}) {
		t.Fatalf("ProcessIdentity(child) = %+v, %v, %v; want ses_a", id, ok, err)
	}
	// Tests never use the user manager, so the child has no scope, and an
	// empty unit matches nothing.
	if _, ok, err := s.ScopeIdentity(""); err != nil || ok {
		t.Fatalf("ScopeIdentity(\"\") = %v, %v; want no match", ok, err)
	}
	// The real /proc resolves the child to its session.
	r, err := New("/proc", os.Getpid(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(cmd.Process.Pid); got.Kind != Session || got.Session != "ses_a" {
		t.Fatalf("Resolve(child) = %+v, want ses_a", got)
	}
}

func TestRecordDropsEndedProcessesAfterADay(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	ended := Process{PID: 0, Start: 1} // /proc/0 never exists
	if err := s.record(context.Background(), base.Add(-25*time.Hour), "dyna-old-1.scope", ended, Identity{Kind: Session, Session: "ses_old"}, "turn"); err != nil {
		t.Fatal(err)
	}
	recent := Process{PID: 0, Start: 2}
	if err := s.record(context.Background(), base.Add(-time.Hour), "", recent, Identity{Kind: Session, Session: "ses_recent"}, "shell"); err != nil {
		t.Fatal(err)
	}
	self, _, err := readStat("/proc", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.record(context.Background(), base, "", self, Identity{Kind: Worker, Run: "run_live"}, "workflow"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p    Process
		want bool
	}{{ended, false}, {recent, true}, {self, true}} {
		if _, ok, err := s.ProcessIdentity(tc.p); err != nil || ok != tc.want {
			t.Fatalf("record for %+v kept = %v (%v), want %v", tc.p, ok, err, tc.want)
		}
	}
	if _, ok, _ := s.ScopeIdentity("dyna-old-1.scope"); ok {
		t.Fatal("the ended scope's record was kept")
	}
}

func TestStartStopsAnUnrecordedProcess(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := startChild(t, Launch{DataDir: notADir, Name: "ses_b", Actor: Identity{Kind: Session, Session: "ses_b"}, Launcher: "turn"})
	if err == nil {
		t.Fatal("Start succeeded without a record")
	}
	if cmd.ProcessState == nil {
		t.Fatal("the unrecorded process still runs")
	}
}

// helperEnv makes this test binary act as a launched dyna process.
const helperEnv = "CALLERID_GATE_HELPER"

// TestLaunchGateHelper runs only as the child of TestStartHoldsTheChild.
// It reports whether its own record existed when the gate opened.
func TestLaunchGateHelper(t *testing.T) {
	dir := os.Getenv(helperEnv)
	if dir == "" {
		t.Skip("runs only as a launched child")
	}
	WaitForLaunch()
	s, err := OpenStore(dir)
	if err != nil {
		os.Exit(3)
	}
	self, _, err := readStat("/proc", os.Getpid())
	if err != nil {
		os.Exit(3)
	}
	if _, ok, err := s.ProcessIdentity(self); err != nil || !ok {
		os.Exit(4)
	}
	if _, held := os.LookupEnv(LaunchGate); held {
		os.Exit(5)
	}
	os.Exit(0)
}

// A launched process must not run before its record commits, or its first
// engine call would be refused.
func TestStartHoldsTheChildUntilTheRecordCommits(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchGateHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"="+dir)
	if err := Start(context.Background(), cmd, Launch{DataDir: dir, Name: "ses_c", Actor: Identity{Kind: Session, Session: "ses_c"}, Launcher: "turn"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the child ran before its record or kept the gate variable: %v", err)
	}
}

// A child whose launcher closes the gate without recording it exits.
func TestWaitForLaunchExitsWithoutARecord(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchGateHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"="+dir)
	if err := Start(context.Background(), cmd, Launch{DataDir: notADir, Actor: Identity{Kind: Session, Session: "ses_d"}, Launcher: "turn"}); err == nil {
		t.Fatal("Start succeeded without a record")
	}
	if cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatalf("an unrecorded child ran to success: %v", cmd.ProcessState)
	}
}

// A child that exits before its gate opens is still recorded, and Start
// reports no record error for it.
func TestStartAcceptsAChildThatAlreadyExited(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/true")
	if err := Start(context.Background(), cmd, Launch{DataDir: dir, Name: "ses_e", Actor: Identity{Kind: Session, Session: "ses_e"}, Launcher: "turn"}); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

// A child whose record failed must never get the byte that lets it run.
func TestReleaseOpensTheGateOnlyAfterARecord(t *testing.T) {
	for _, recorded := range []error{nil, errors.New("database is locked")} {
		gate, held, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := release(held, recorded); err != recorded {
			t.Fatalf("release(%v) = %v", recorded, err)
		}
		got, err := io.ReadAll(gate)
		gate.Close()
		if err != nil || (len(got) == 1) != (recorded == nil) {
			t.Fatalf("after record error %v the child read %q, want one byte only after a record", recorded, got)
		}
	}
}

// A record holds exactly one actor. Owner records are kept to the detached
// launches that run a command for whoever ran it.
func TestLaunchChecksTheActor(t *testing.T) {
	for _, tc := range []struct {
		launcher string
		actor    Identity
		ok       bool
	}{
		{"turn", Identity{Kind: Session, Session: "ses_a"}, true},
		{"workflow", Identity{Kind: Worker, Run: "run_a"}, true},
		{"workflow", Identity{Kind: Worker, Run: "run_a", Agent: 2}, true},
		{"consolidate", Identity{Kind: Owner}, true},
		{"transfer", Identity{Kind: Owner}, true},
		{"resume-room", Identity{Kind: Owner}, true},
		{"turn", Identity{Kind: Session}, false},
		{"workflow", Identity{Kind: Session, Session: "run_a"}.with(Worker), false},
		{"workflow", Identity{Kind: Worker}, false},
		{"turn", Identity{Kind: Owner}, false},
		{"shell", Identity{Kind: Owner}, false},
		{"workflow", Identity{Kind: Owner}, false},
		{"hook", Identity{Kind: Owner}, false},
		{"hook identity", Identity{Kind: Owner}, false},
		{"turn", Identity{Kind: Refused}, false},
	} {
		if err := (Launch{Actor: tc.actor, Launcher: tc.launcher}).check(); (err == nil) != tc.ok {
			t.Errorf("a %s launch for %+v: error %v, want allowed=%v", tc.launcher, tc.actor, err, tc.ok)
		}
	}
	cmd := exec.Command("/bin/true")
	if err := Start(context.Background(), cmd, Launch{DataDir: t.TempDir(), Actor: Identity{Kind: Owner}, Launcher: "turn"}); err == nil || cmd.Process != nil {
		t.Fatalf("an owner turn launch started (%v)", err)
	}
}

// with sets a kind on a copy, so the table can show a wrong pairing.
func (id Identity) with(k Kind) Identity { id.Kind = k; return id }

// A workflow worker resolves to its run, never to a session or the owner;
// a detached launch the owner ran resolves to the owner.
func TestRecordedActorsResolve(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		launcher string
		actor    Identity
	}{
		{"workflow", Identity{Kind: Worker, Run: "run_20261020-000000-abcd"}},
		{"transfer", Identity{Kind: Owner}},
	} {
		cmd, err := startChild(t, Launch{DataDir: dir, Name: tc.launcher, Actor: tc.actor, Launcher: tc.launcher})
		if err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		r, err := New("/proc", os.Getpid(), s)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Resolve(cmd.Process.Pid); got != tc.actor {
			t.Fatalf("a %s launch resolves to %+v, want %+v", tc.launcher, got, tc.actor)
		}
	}
}

// A launch record goes through the process's one Writer, so the engine
// publishes it and counts it in Health.
func TestRecordUsesTheProcessWriter(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var published []hub.Event
	state.For(db).SetPublish(func(c hub.Event) { published = append(published, c) })
	if _, err := startChild(t, Launch{DataDir: dir, Name: "ses_f", Actor: Identity{Kind: Session, Session: "ses_f"}, Launcher: "turn"}); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].Topic != "callers" {
		t.Fatalf("the engine's writer published %+v, want one callers change", published)
	}
}

// A launcher under a recorded session runner is that session even with no
// DYNA_* variables, the mistake behind g3411; the environment never decides.
func TestSelfIgnoresTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no engine answers here
	storetest.ClearStamp(t)
	t.Setenv("DYNA_OWNER_ENV", "")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	runner, _, err := readStat("/proc", os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	session := Identity{Kind: Session, Session: "ses_runner"}
	if err := s.record(context.Background(), time.Now(), "", runner, session, "turn"); err != nil {
		t.Fatal(err)
	}
	if got, err := Self(dir); err != nil || got != session {
		t.Fatalf("Self() = %+v, %v; want the recorded runner's session", got, err)
	}
}

// A workflow agent's harness CLI is not dyna, so its run-worker records it
// right after it starts. The agent, and every command it runs, then resolves
// to its own agent number, not to the run's agent 0.
func TestRecordStartedResolvesTheAgent(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "read line")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
	agent := Identity{Kind: Worker, Run: "run_a", Agent: 2}
	if err := RecordStarted(context.Background(), cmd.Process.Pid, Launch{DataDir: dir, Name: "run_a", Actor: agent, Launcher: "workflow"}); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New("/proc", os.Getpid(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(cmd.Process.Pid); got != agent {
		t.Fatalf("Resolve(agent) = %+v, want %+v", got, agent)
	}
	// A row from before agent numbers holds "" in the worker column: the run.
	if _, err := s.db.Exec(`INSERT INTO caller_records(unit,pid,start,actor,session,run,worker,launcher,recorded) VALUES('',0,7,'worker','','run_b','','workflow',0)`); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.ProcessIdentity(Process{PID: 0, Start: 7}); err != nil || !ok || got != (Identity{Kind: Worker, Run: "run_b"}) {
		t.Fatalf("an old row = %+v, %v, %v; want run_b itself", got, ok, err)
	}
}

// A launch records the StoreFormat of the binary that made it, so a watcher
// can tell a runner of an earlier release from one of this release without
// reading its executable. A row from an earlier release reads as format 0.
func TestLaunchRecordsTheStoreFormat(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	current, earlier := Process{PID: 1001, Start: 1}, Process{PID: 1002, Start: 1}
	if err := s.record(context.Background(), now, "", current, Identity{Kind: Session, Session: "ses_new"}, "turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO caller_records(unit,pid,start,actor,session,run,worker,launcher,recorded) VALUES('',?,?,?,?,'','','turn',?)`, earlier.PID, int64(earlier.Start), Session, "ses_old", now.Unix()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p       Process
		session string
		format  int
	}{{current, "ses_new", StoreFormat}, {earlier, "ses_old", 0}} {
		l, ok, err := s.ProcessLaunch(tc.p)
		if err != nil || !ok || l.Session != tc.session || l.Format != tc.format {
			t.Errorf("ProcessLaunch(%v) = %+v, %v, %v; want %s with format %d", tc.p, l, ok, err, tc.session, tc.format)
		}
	}
}

// The owner shell is recorded by PID and birth time, never by engine cgroup.
func TestOwnerTerminalDescendantKeepsOwnerIdentity(t *testing.T) {
	t.Cleanup(authority.InEngine())
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	current, err := ProcessOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-l")
	var inherited *os.File
	if err := StartTerminal(t.Context(), cmd, dir, func() error {
		if cmd.Path != "/bin/sh" || len(cmd.ExtraFiles) != 1 || len(cmd.Args) != 6 || cmd.Args[5] != "-l" {
			t.Fatalf("shell was not gated: %+v", cmd)
		}
		fd, err := syscall.Dup(int(cmd.ExtraFiles[0].Fd()))
		if err != nil {
			return err
		}
		inherited = os.NewFile(uintptr(fd), "inherited terminal gate")
		if err := syscall.SetNonblock(fd, true); err != nil {
			return err
		}
		if n, err := syscall.Read(fd, make([]byte, 1)); n != -1 || err != syscall.EAGAIN {
			t.Fatalf("terminal gate opened before recording: %d %v", n, err)
		}
		cmd.Process = &os.Process{Pid: current.PID} // Fake starter: no subprocess.
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer inherited.Close()
	gate, err := io.ReadAll(inherited)
	if err != nil || string(gate) != "ready\n" {
		t.Fatalf("gate = %q, %v", gate, err)
	}
	saved, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolver, proc := newResolver(t, engineCG)
	resolver.records = saved
	proc.add(current.PID, engine, current.Start, engineCG)
	proc.add(600, current.PID, 60, engineCG)
	proc.add(601, engine, 61, engineCG)
	if got := resolver.Resolve(600); got != (Identity{Kind: Owner}) {
		t.Fatalf("owner terminal descendant = %+v", got)
	}
	if got := resolver.Resolve(601); got.Kind != Refused {
		t.Fatalf("unrelated engine child = %+v", got)
	}
	proc.add(current.PID, engine, current.Start+1, engineCG)
	if got := resolver.Resolve(600); got.Kind != Refused {
		t.Fatalf("reused terminal PID = %+v", got)
	}
}
