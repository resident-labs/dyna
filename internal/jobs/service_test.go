package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobstest"
	"dyna-agent/internal/machines"
)

// fakeMachines answers the machine registry from one fixed set.
type fakeMachines struct {
	local  machines.Machine
	byName map[string]machines.Machine
}

func (f fakeMachines) Get(_ context.Context, name string) (machines.Machine, error) {
	if m, ok := f.byName[name]; ok {
		return m, nil
	}
	return machines.Machine{}, os.ErrNotExist
}

func (f fakeMachines) GetLocal(context.Context) (machines.Machine, error) { return f.local, nil }

// chanReporter hands each event to a test through a channel, so a test waits
// for the service by receiving, with no clock of its own.
type chanReporter struct {
	started  chan Job
	finished chan Job
}

func newChanReporter() *chanReporter {
	return &chanReporter{started: make(chan Job, 4), finished: make(chan Job, 4)}
}

func (r *chanReporter) JobStarted(_ context.Context, job Job) error {
	r.started <- job
	return nil
}

func (r *chanReporter) JobFinished(_ context.Context, job Job) error {
	r.finished <- job
	return nil
}

// newService wires a service to one fake node and a store in a temp folder.
// The legacy function answers every call that is not the detached protocol,
// such as git and the folder removal. runStarted tells a test the service
// began to run a job.
func newService(t *testing.T, legacy jobstest.RunFunc) (*Service, *jobstest.Fake, *chanReporter, chan Job) {
	t.Helper()
	dir := t.TempDir()
	machine := machines.Machine{Name: "here", Enabled: true, CISlots: 2}
	reporter := newChanReporter()
	runStarted := make(chan Job, 4)
	svc := &Service{
		Store:    Store{DataDir: dir},
		Machines: fakeMachines{local: machine, byName: map[string]machines.Machine{"here": machine}},
		Reporter: reporter,
		DataDir:  dir,
		logf:     func(string, ...any) {},
		onRun:    func(job Job) { runStarted <- job },
	}
	fake := jobstest.NewFake(t, jobstest.Protocol{Start: StartScript, Poll: PollScript, Stop: StopScript}, legacy)
	svc.NewExecutor = func(conn Conn) Executor {
		x := Executor{Node: conn}
		x.Command, x.Wait, x.Clock = Command(fake.CommandFunc()), fake.WaitFunc(), fake.ClockFunc()
		return x
	}
	return svc, fake, reporter, runStarted
}

// exitWith is a command that exited with a status, as a real one does.
func exitWith(t *testing.T, code int) error {
	t.Helper()
	return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
}

// A job that passes records its log, its exit code, and both events, and its
// folder on the node is removed at the end.
func TestJobPassesEndToEnd(t *testing.T) {
	removed := make(chan string, 8)
	svc, _, reporter, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, stderr io.Writer, name string, args ...string) error {
		script := name + " " + strings.Join(args, " ")
		if strings.Contains(script, "rm -rf -- ") {
			fields := strings.Fields(script)
			removed <- fields[len(fields)-1]
		}
		fmt.Fprintln(out, "hello from the job")
		return nil
	})
	defer svc.Stop()
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"echo", "hi"}, Host: true})
	if err != nil {
		t.Fatal(err)
	}
	if state := (<-reporter.finished).State; state != StatePassed {
		t.Fatalf("finished event = %s, want passed", state)
	}
	stored, err := svc.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExitCode == nil || *stored.ExitCode != 0 {
		t.Fatalf("exit = %v, want 0", stored.ExitCode)
	}
	log, err := os.ReadFile(stored.LogPath)
	if err != nil || !strings.Contains(string(log), "hello from the job") {
		t.Fatalf("log = %q %v, want the job's output", log, err)
	}
	if started := <-reporter.started; started.ID != job.ID {
		t.Fatalf("started event = %s, want the submitted job", started.ID)
	}
	select {
	case dir := <-removed:
		if !strings.Contains(dir, "/jobs/"+job.ID) {
			t.Fatalf("removed %q, want the job's folder", dir)
		}
	case <-t.Context().Done():
		t.Fatal("the job's folder was not removed from the node")
	}
}

// A failing command fails the job and keeps its exit code.
func TestJobFailureKeepsItsExitCode(t *testing.T) {
	svc, _, reporter, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "boom")
		return exitWith(t, 3)
	})
	defer svc.Stop()
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"false"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = job
	_ = job
	finished := <-reporter.finished
	if finished.State != StateFailed || finished.ExitCode == nil || *finished.ExitCode != 3 {
		t.Fatalf("finished event = %s exit %v, want failed exit 3", finished.State, finished.ExitCode)
	}
}

// Cancelling a running job stops its unit on the node and records cancelled.
func TestCancelStopsTheJob(t *testing.T) {
	svc, fake, reporter, runStarted := newService(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "working")
		<-ctx.Done()
		return ctx.Err()
	})
	defer svc.Stop()
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"sleep", "100"}})
	if err != nil {
		t.Fatal(err)
	}
	<-runStarted
	if _, err := svc.Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if state := (<-reporter.finished).State; state != StateCancelled {
		t.Fatalf("finished event = %s, want cancelled", state)
	}
	if fake.StopCount() == 0 {
		t.Fatal("the node never saw a stop call")
	}
}

// A running row whose engine died reattaches instead of launching again: the
// node sees no second start, and the job still ends through the new engine.
func TestEngineRestartReattachesWithoutASecondStart(t *testing.T) {
	svc, fake, reporter, _ := newService(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "before the restart")
		<-ctx.Done()
		return ctx.Err()
	})
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	<-reporter.started
	// The engine stops without cancelling the job: the row stays running and
	// the restarted engine reattaches.
	SetEngineStoppingForTest(true)
	svc.Stop()
	SetEngineStoppingForTest(false)
	stored, err := svc.Store.Get(t.Context(), job.ID)
	if err != nil || stored.State != StateRunning {
		t.Fatalf("row = %s, %v; want running after an engine stop", stored.State, err)
	}
	starts := len(fake.StartCalls())
	reporter2 := newChanReporter()
	runStarted2 := make(chan Job, 4)
	restarted := &Service{
		Store: svc.Store, Machines: svc.Machines, Reporter: reporter2, DataDir: svc.DataDir,
		logf: func(string, ...any) {}, onRun: func(job Job) { runStarted2 <- job },
	}
	restarted.NewExecutor = svc.NewExecutor
	restarted.Start(t.Context())
	<-runStarted2
	if got := len(fake.StartCalls()); got != starts {
		t.Fatalf("start calls = %d after the restart, want the same %d", got, starts)
	}
	if _, err := restarted.Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if state := (<-reporter2.finished).State; state != StateCancelled {
		t.Fatalf("finished event = %s, want cancelled through the new engine", state)
	}
	restarted.Stop()
}

// A queued job that is cancelled before the engine picks it up never starts.
func TestCancelOfAQueuedJobSkipsIt(t *testing.T) {
	svc, fake, _, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "ran")
		return nil
	})
	defer svc.Stop()
	job := Job{ID: "job_queued", RoomID: "dyna", Node: "here", Command: []string{"echo"}, State: StateQueued, Timeout: time.Minute, Created: time.Unix(1, 0), LogPath: filepath.Join(svc.DataDir, "jobs", "job_queued", "log")}
	if err := svc.Store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	stopped, err := svc.Cancel(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != StateCancelled || len(fake.StartCalls()) != 0 {
		t.Fatalf("cancel = %s with %d starts, want cancelled with none", stopped.State, len(fake.StartCalls()))
	}
}

// Host execution on another machine needs that machine's host-check grant;
// the local node needs none.
func TestHostJobNeedsAGrantOnARemoteMachine(t *testing.T) {
	svc, _, _, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "ran")
		return nil
	})
	defer svc.Stop()
	svc.Machines = fakeMachines{
		local:  machines.Machine{Name: "here", Enabled: true},
		byName: map[string]machines.Machine{"here": {Name: "here", Enabled: true}, "pve": {Name: "pve", SSH: "pip@pve", Enabled: true, CISlots: 2}},
	}
	if _, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"make"}, Host: true, Node: "pve"}); err == nil {
		t.Fatal("a host job on a remote machine without a grant was accepted")
	}
	granted := machines.Machine{Name: "pve", SSH: "pip@pve", Enabled: true, CISlots: 2, HostChecksGrants: []machines.HostChecksGrant{{By: machines.GrantorOwner}}}
	svc.Machines = fakeMachines{local: machines.Machine{Name: "here", Enabled: true}, byName: map[string]machines.Machine{"here": {Name: "here", Enabled: true}, "pve": granted}}
	if _, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"make"}, Host: true, Node: "pve"}); err != nil {
		t.Fatalf("a granted host job was refused: %v", err)
	}
}

// ParseMemory reads the sizes a person writes.
func TestParseMemory(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"16g", 16 * 1024},
		{"512m", 512},
		{"4096", 4096},
		{"2G", 2 * 1024},
	}
	for _, tc := range cases {
		got, err := ParseMemory(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseMemory(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "lots", "0g", "-4g"} {
		if got, err := ParseMemory(bad); err == nil {
			t.Fatalf("ParseMemory(%q) = %d, want an error", bad, got)
		}
	}
}

// classify names every end state from its run error.
func TestClassify(t *testing.T) {
	deadline := fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
	cases := []struct {
		err   error
		state State
	}{
		{nil, StatePassed},
		{context.DeadlineExceeded, StateTimedOut},
		{deadline, StateTimedOut},
		{context.Canceled, StateCancelled},
		{&DetachedExit{Code: 0}, StatePassed},
		{&DetachedExit{Code: 3}, StateFailed},
		{&DetachedExit{Code: 124}, StateTimedOut},
		{&NodeLostError{Node: "pve", Gone: true}, StateLost},
		{errors.New("start failed"), StateFailed},
	}
	for _, tc := range cases {
		state, _ := classify(tc.err)
		if state != tc.state {
			t.Fatalf("classify(%v) = %s, want %s", tc.err, state, tc.state)
		}
	}
}

// The store round-trips a row with every field.
func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := Store{DataDir: dir}
	job := Job{
		ID: "job_round", RoomID: "dyna", Session: "ses_1", Node: "here",
		Command: []string{"echo", "with spaces"}, Repo: "/repo/.git", SHA: strings.Repeat("a", 40),
		Host: true, CPUs: 4, MemoryMiB: 8192, Timeout: 90 * time.Minute,
		State: StateRunning, LogPath: filepath.Join(dir, "log"),
		Created: time.Unix(1000, 0), Started: time.Unix(1001, 0),
		Dir: "/node/jobs/job_round", Unit: "dyna-job-job_round.service", LogBytes: 42,
		Note: "Dyna clamped cpus",
	}
	if err := st.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Session != job.Session || !got.Host || got.Timeout != job.Timeout || got.SHA != job.SHA {
		t.Fatalf("round trip = %+v", got)
	}
	if len(got.Command) != 2 || got.Command[1] != "with spaces" {
		t.Fatalf("command = %q, want the words with spaces", got.Command)
	}
	if got.Dir != job.Dir || got.Unit != job.Unit || got.LogBytes != 42 {
		t.Fatalf("reattach fields = %+v", got)
	}
	list, err := st.List(t.Context(), false)
	if err != nil || len(list) != 1 || list[0].ID != job.ID {
		t.Fatalf("open list = %v, %v", list, err)
	}
}
