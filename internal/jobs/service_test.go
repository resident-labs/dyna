package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
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
	queued   chan Job
	started  chan Job
	finished chan Job
}

func newChanReporter() *chanReporter {
	return &chanReporter{queued: make(chan Job, 4), started: make(chan Job, 4), finished: make(chan Job, 4)}
}

func (r *chanReporter) JobQueued(_ context.Context, job Job) error {
	r.queued <- job
	return nil
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
// newIdleService builds a service that no engine started, as a standby
// engine's service is: a Submit only queues.
func newIdleService(t *testing.T, legacy jobstest.RunFunc) (*Service, *jobstest.Fake, *chanReporter, chan Job) {
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

// newService is the service of the engine that owns the worker lock: it
// claims and starts what is submitted to it.
func newService(t *testing.T, legacy jobstest.RunFunc) (*Service, *jobstest.Fake, *chanReporter, chan Job) {
	t.Helper()
	svc, fake, reporter, runStarted := newIdleService(t, legacy)
	svc.Start(t.Context())
	return svc, fake, reporter, runStarted
}

// blockingJob is a job that prints one line and runs until its context
// ends, while folder cleanup and other node upkeep calls return at once.
func blockingJob(line string) jobstest.RunFunc {
	return func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, line)
		<-ctx.Done()
		return ctx.Err()
	}
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
	onNode := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "working")
		onNode <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	svc, fake, reporter, _ := newService(t, legacy)
	defer svc.Stop()
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"sleep", "100"}})
	if err != nil {
		t.Fatal(err)
	}
	// The start event goes out before the node work, so wait for the node
	// itself: the cancel below must land while the job runs.
	<-onNode
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

// A running job whose log stays unchanged for its idle duration is stopped
// and ends through the one usual finished event.
func TestIdleTimeoutStopsTheJob(t *testing.T) {
	svc, fake, reporter, _ := newService(t, blockingJob("working"))
	defer svc.Stop()
	idle := 5 * time.Second
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}, Idle: &idle, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	finished := <-reporter.finished
	if finished.ID != job.ID || finished.State != StateTimedOut || finished.Note != "no output for 5s" {
		t.Fatalf("finished = %+v, want one idle timeout", finished)
	}
	if fake.StopCount() != 1 {
		t.Fatalf("stop calls = %d, want 1", fake.StopCount())
	}
	select {
	case extra := <-reporter.finished:
		t.Fatalf("unexpected second finished event: %+v", extra)
	default:
	}
}

// A failed idle stop leaves the job open. The existing poll retries the stop
// and only the confirmed stop produces the finished event.
func TestIdleTimeoutRetriesFailedStop(t *testing.T) {
	svc, fake, reporter, _ := newService(t, blockingJob("working"))
	defer svc.Stop()
	fake.FailStops(1)
	idle := 5 * time.Second
	_, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}, Idle: &idle, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if finished := <-reporter.finished; finished.State != StateTimedOut || finished.Note != "no output for 5s" {
		t.Fatalf("finished = %+v, want timed_out after the retry", finished)
	}
	if got := fake.StopCount(); got != 2 {
		t.Fatalf("stop calls = %d, want the failed call and one retry", got)
	}
}

// Cancelling an idle run does not end its row while every stop is failing.
// The existing poll keeps retrying until the node confirms the unit stopped.
func TestIdleStopSurvivesRunCancellation(t *testing.T) {
	svc, fake, reporter, _ := newService(t, blockingJob("working"))
	defer svc.Stop()
	fake.FailStops(1_000_000)
	idle := 5 * time.Second
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}, Idle: &idle, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for fake.StopCount() == 0 {
		runtime.Gosched()
	}
	calls := fake.StopCount()
	if _, err := svc.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	// Two more calls prove that the cancelled run is still reconciling its
	// idle stop. A finished event here is the bug: the unit may still run.
	for fake.StopCount() < calls+2 {
		select {
		case finished := <-reporter.finished:
			t.Fatalf("finished before the idle stop was confirmed: %+v", finished)
		default:
		}
		runtime.Gosched()
	}
	target := calls + 2
	fake.FailStops(0)
	if finished := <-reporter.finished; finished.State != StateCancelled || finished.Note != "cancelled" {
		t.Fatalf("finished = %+v, want cancellation after the confirmed stop", finished)
	}
	if fake.StopCount() <= target {
		t.Fatalf("stop calls = %d, want a successful call after the failures", fake.StopCount())
	}
}

// An idle stop drains every log chunk that the regular 256 KiB polls have
// not copied yet before the finished event.
func TestIdleTimeoutDrainsRemainingLog(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 2<<20)
	svc, _, reporter, _ := newService(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			return nil
		}
		if _, err := out.Write(payload); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	defer svc.Stop()
	idle := 5 * time.Second
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}, Idle: &idle, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if finished := <-reporter.finished; finished.State != StateTimedOut {
		t.Fatalf("finished = %+v, want timed_out", finished)
	}
	stored, err := svc.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(stored.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != len(payload) {
		t.Fatalf("log has %d bytes, want all %d after the idle stop", len(log), len(payload))
	}
}

// Output before each idle deadline keeps a running job alive, even after the
// retained log reached its cap and stopped growing.
func TestOutputAfterLogCapPreventsIdleTimeout(t *testing.T) {
	var fake *jobstest.Fake
	svc, fake, reporter, _ := newService(t, func(ctx context.Context, _ string, _ io.Reader, _, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			return nil
		}
		for fake.PollCount() < 4 && ctx.Err() == nil {
			runtime.Gosched()
		}
		return ctx.Err()
	})
	defer svc.Stop()
	fake.CapLogs()
	fake.OutputEveryPoll()
	idle := 5 * time.Second
	_, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"build"}, Idle: &idle, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if finished := <-reporter.finished; finished.State != StatePassed {
		t.Fatalf("finished = %+v, want output to keep the job alive", finished)
	}
}

// The submit boundary rejects a non-positive idle duration and one beyond the
// job's own deadline. Zero on Job, rather than Input, means no idle timeout.
func TestIdleTimeoutValidation(t *testing.T) {
	svc, _, _, _ := newIdleService(t, nil)
	for _, idle := range []time.Duration{0, -time.Second} {
		if _, err := svc.Submit(t.Context(), Input{Command: []string{"true"}, Idle: &idle}); err == nil {
			t.Fatalf("idle %s was accepted, want a positive duration", idle)
		}
	}
	idle, timeout := 10*time.Minute, 5*time.Minute
	if _, err := svc.Submit(t.Context(), Input{Command: []string{"true"}, Idle: &idle, Timeout: timeout}); err == nil {
		t.Fatal("an idle timeout beyond the job timeout was accepted")
	}
}

// A running row whose engine died reattaches instead of launching again: the
// node sees no second start, and the job still ends through the new engine.
func TestEngineRestartReattachesWithoutASecondStart(t *testing.T) {
	onNode := make(chan struct{})
	onNodeLegacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "before the restart")
		onNode <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	svc, fake, _, _ := newService(t, onNodeLegacy)
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	// The start event goes out before the node work, so wait for the node
	// itself: the stop below must land while the job runs.
	<-onNode
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

// The node log's modification time, not the restarted engine's start time,
// carries the idle deadline across an engine restart.
func TestIdleDeadlineSurvivesEngineRestart(t *testing.T) {
	onNode := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			return nil
		}
		fmt.Fprintln(out, "working")
		onNode <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	svc, fake, _, _ := newService(t, legacy)
	idle := time.Hour
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}, Idle: &idle, Timeout: 3 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	<-onNode
	SetEngineStoppingForTest(true)
	svc.Stop()
	SetEngineStoppingForTest(false)
	if err := fake.WaitFunc()(t.Context(), 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	reporter := newChanReporter()
	restarted := &Service{
		Store: svc.Store, Machines: svc.Machines, Reporter: reporter, DataDir: svc.DataDir,
		logf: func(string, ...any) {},
	}
	restarted.NewExecutor = svc.NewExecutor
	restarted.Start(t.Context())
	defer restarted.Stop()
	finished := <-reporter.finished
	if finished.ID != job.ID || finished.State != StateTimedOut || finished.Note != "no output for 1h0m0s" {
		t.Fatalf("finished after restart = %+v, want the stored idle deadline", finished)
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
		Host: true, CPUs: 4, MemoryMiB: 8192, Timeout: 90 * time.Minute, Idle: 20 * time.Minute,
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
	if got.Session != job.Session || !got.Host || got.Timeout != job.Timeout || got.Idle != job.Idle || got.SHA != job.SHA {
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

// Named resources used version 4. Their existing databases still need the
// idle column when this store moves them to version 5.
func TestStoreMigrationAddsIdleToVersionFour(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE jobs(id TEXT PRIMARY KEY, resources TEXT NOT NULL DEFAULT '[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES('jobs.version','4')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name='idle_s'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("the version 4 database did not gain idle_s")
	}
	var version string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='jobs.version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "5" {
		t.Fatalf("jobs.version = %s, want 5", version)
	}
}
