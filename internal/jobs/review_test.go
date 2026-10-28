package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobstest"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/shellquote"
)

// TestContainerJobMountsItsCheckout is the fail-first test for review finding
// 1: a container job with a commit must mount its checkout and work there.
func TestContainerJobMountsItsCheckout(t *testing.T) {
	sha := strings.Repeat("a", 40)
	svc, fake, reporter, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "rev-parse --verify"):
			fmt.Fprintln(out, sha)
		case strings.Contains(joined, "--git-common-dir"):
			fmt.Fprintln(out, "/repo/.git")
		default:
			fmt.Fprintln(out, "ran")
		}
		return nil
	})
	defer svc.Stop()
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"make", "test"}, Image: "golang:1", Repo: "/repo", Ref: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	<-reporter.finished
	starts := fake.StartCalls()
	_ = job
	if len(starts) == 0 {
		t.Fatal("the job never started")
	}
	script := starts[0].Script
	if !strings.Contains(script, "-v ") || !strings.Contains(script, "/workspace") || !strings.Contains(script, "-w /workspace") {
		t.Fatalf("container script mounts no checkout and sets no workdir:\n%s", script)
	}
}

// TestCleanupRunsOnTheNode is the fail-first test for review finding 2: the
// end-of-job folder removal must reach the job's node, not this machine.
func TestCleanupRunsOnTheNode(t *testing.T) {
	dir := t.TempDir()
	machine := machines.Machine{Name: "pve", SSH: "pip@pve", Enabled: true, CISlots: 2, HostChecksGrants: []machines.HostChecksGrant{{By: machines.GrantorOwner}}}
	reporter := newChanReporter()
	svc := &Service{
		Store:    Store{DataDir: dir},
		Machines: fakeMachines{local: machine, byName: map[string]machines.Machine{"pve": machine}},
		Reporter: reporter,
		DataDir:  dir,
		logf:     func(string, ...any) {},
	}
	var mu chanMutex
	var localRemovals, remoteRemovals int
	svc.Start(t.Context())
	fake := jobstest.NewFake(t, jobstest.Protocol{Start: StartScript, Poll: PollScript, Stop: StopScript}, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		if !strings.Contains(joined, "rm -rf -- ") {
			fmt.Fprintln(out, "ran")
			return nil
		}
		mu.lock()
		defer mu.unlock()
		if name == "ssh" {
			remoteRemovals++
		} else {
			localRemovals++
		}
		return nil
	})
	svc.NewExecutor = func(conn Conn) Executor {
		x := Executor{Node: conn}
		x.Command, x.Wait, x.Clock = Command(fake.CommandFunc()), fake.WaitFunc(), fake.ClockFunc()
		return x
	}
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}, Node: "pve"})
	if err != nil {
		t.Fatal(err)
	}
	_ = job
	<-reporter.finished
	mu.lock()
	defer mu.unlock()
	if localRemovals != 0 || remoteRemovals != 1 {
		t.Fatalf("folder removals: %d local, %d remote; want 0 local and 1 on the node", localRemovals, remoteRemovals)
	}
}

type chanMutex struct{ ch chan struct{} }

func (m *chanMutex) lock() {
	if m.ch == nil {
		m.ch = make(chan struct{}, 1)
	}
	m.ch <- struct{}{}
}

func (m *chanMutex) unlock() { <-m.ch }

// TestLogOffsetSurvivesEngineStop is the fail-first test for review finding
// 3: the row's stored log offset after an engine stop matches the log written
// so far, and the restarted engine never repeats output.
func TestLogOffsetSurvivesEngineStop(t *testing.T) {
	svc, _, reporter, _ := newService(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "before the restart")
		<-ctx.Done()
		return ctx.Err()
	})
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	<-reporter.started
	SetEngineStoppingForTest(true)
	svc.Stop()
	SetEngineStoppingForTest(false)
	stored, err := svc.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(stored.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LogBytes != int64(len(log)) {
		t.Fatalf("stored offset %d after the stop, want the %d bytes of the log", stored.LogBytes, len(log))
	}
	// The restarted engine reattaches; the log must hold the old line once.
	reporter2 := newChanReporter()
	restarted := &Service{Store: svc.Store, Machines: svc.Machines, Reporter: reporter2, DataDir: svc.DataDir, logf: func(string, ...any) {}}
	restarted.NewExecutor = svc.NewExecutor
	restarted.Start(t.Context())
	if _, err := restarted.Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	<-reporter2.finished
	log, err = os.ReadFile(stored.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "before the restart"); n != 1 {
		t.Fatalf("the pre-restart line appears %d times in the log, want once:\n%s", n, log)
	}
	final, _ := restarted.Store.Get(t.Context(), job.ID)
	if final.LogBytes != int64(len(log)) {
		t.Fatalf("final offset %d, want the %d bytes of the log", final.LogBytes, len(log))
	}
	restarted.Stop()
}

// TestCommandWithControlBytesRoundTrips is the fail-first test for review
// finding 4: a command word with a control byte must round-trip through the
// stored row.
func TestCommandWithControlBytesRoundTrips(t *testing.T) {
	dir := t.TempDir()
	st := Store{DataDir: dir}
	job := Job{
		ID: "job_ctl", Node: "here", State: StateQueued, Timeout: time.Minute,
		Command: []string{"printf", "a\x01b"}, Created: time.Unix(1, 0),
		LogPath: filepath.Join(dir, "log"),
	}
	if err := st.Save(t.Context(), job); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := st.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("a control byte broke the row: %v", err)
	}
	if len(got.Command) != 2 || got.Command[1] != "a\x01b" {
		t.Fatalf("command = %#v, want the control byte back", got.Command)
	}
	// The stored form is a JSON array.
	var raw []string
	if err := json.Unmarshal([]byte(storedCommand(t, st, job.ID)), &raw); err != nil {
		t.Fatalf("the stored command is not JSON: %v", err)
	}
}

// storedCommand reads the raw command column of one row.
func storedCommand(t *testing.T, st Store, id string) string {
	t.Helper()
	db, err := st.db()
	if err != nil {
		t.Fatal(err)
	}
	row := db.QueryRow("SELECT command FROM jobs WHERE id=?", id)
	var raw string
	if err := row.Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestJobArgumentsKeepTheirBoundaries is the fail-first test for review
// finding 1: the script a job runs must build exactly the argv the submitter
// passed, so an argument's boundaries and metacharacters survive.
func TestJobArgumentsKeepTheirBoundaries(t *testing.T) {
	svc, fake, reporter, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "ran")
		return nil
	})
	defer svc.Stop()
	_, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"printf", "%s\n", "hello world"}})
	if err != nil {
		t.Fatal(err)
	}
	<-reporter.finished
	starts := fake.StartCalls()
	if len(starts) == 0 {
		t.Fatal("the job never started")
	}
	script := starts[0].Script
	// The command reaches the node shell as one argument of bash -c, with
	// every word quoted on its own inside it, so the shell rebuilds exactly
	// the argv that was submitted.
	words := []string{"printf", "%s\n", "hello world"}
	for i, word := range words {
		words[i] = shellquote.Quote(word)
	}
	want := shellquote.Quote(strings.Join(words, " "))
	if !strings.Contains(script, "bash -o pipefail -c "+want) {
		t.Fatalf("the script builds the wrong argv; want bash -c with the shell-quoted words %s; script:\n%s", want, script)
	}
}

// TestSubmitOnAStandbyEngineOnlyQueues is the fail-first test for review
// finding 2: an engine without the worker lock queues a submission instead of
// starting it, and the owner claims the queued job when it starts.
func TestSubmitOnAStandbyEngineOnlyQueues(t *testing.T) {
	svc, fake, reporter, _ := newIdleService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "ran")
		return nil
	})
	// The run hook of the standby engine fires only if that engine starts the
	// job itself; a queueing engine never runs it.
	standbyRan := make(chan struct{})
	svc.onRun = func(Job) { close(standbyRan) }
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	svc.Stop()
	if n := len(fake.StartCalls()); n != 0 {
		t.Fatalf("a standby engine started %d job(s) itself", n)
	}
	select {
	case done := <-reporter.finished:
		t.Fatalf("a standby engine reported job %s finished", done.ID)
	default:
	}
	stored, err := svc.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != StateQueued {
		t.Fatalf("the row is %s on a standby engine, want queued", stored.State)
	}
	// The owner, the engine that holds the worker lock, claims the job and
	// runs it; the standby engine never runs it at all.
	ownerReporter := newChanReporter()
	owner := &Service{Store: svc.Store, Machines: svc.Machines, Reporter: ownerReporter, DataDir: svc.DataDir, logf: func(string, ...any) {}}
	owner.NewExecutor = svc.NewExecutor
	owner.Start(t.Context())
	defer owner.Stop()
	select {
	case <-standbyRan:
		t.Fatal("the standby engine started the job itself")
	case done := <-ownerReporter.finished:
		if done.State != StatePassed {
			t.Fatalf("the owner ran the queued job to %s, want passed", done.State)
		}
	}
}

// TestReattachRecoversTheOffsetFromTheLogFile is the fail-first test for
// review finding 3: a crash between a poll write and the row's save leaves the
// row offset behind the log file, and the reattached engine recovers the
// offset from the file instead of appending the same output again.
func TestReattachRecoversTheOffsetFromTheLogFile(t *testing.T) {
	svc, fake, _, _ := newIdleService(t, blockingJob("before the restart"))
	delivered := make(chan struct{}, 1)
	base := Command(fake.CommandFunc())
	svc.NewExecutor = func(conn Conn) Executor {
		x := Executor{Node: conn}
		x.Command = func(ctx context.Context, dir string, in io.Reader, out, errw io.Writer, name string, args ...string) error {
			if script, _, ok := jobstest.DecodeNodeScript(name, args); ok && script == PollScript {
				// The poll after the one that carried the line proves the
				// line reached the engine's log file.
				select {
				case delivered <- struct{}{}:
				default:
				}
			}
			return base(ctx, dir, in, out, errw, name, args...)
		}
		x.Wait, x.Clock = fake.WaitFunc(), fake.ClockFunc()
		return x
	}
	svc.Start(t.Context())
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	<-delivered
	<-delivered
	SetEngineStoppingForTest(true)
	svc.Stop()
	SetEngineStoppingForTest(false)
	log, err := os.ReadFile(job.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "before the restart"); n != 1 {
		t.Fatalf("the log holds %d copies of the line before the crash, want 1", n)
	}
	// The crash lost the row's save: the offset is stale while the file holds
	// the line.
	if err := storedExec(svc, "UPDATE jobs SET log_bytes=0 WHERE id=?", job.ID); err != nil {
		t.Fatal(err)
	}
	ownerReporter := newChanReporter()
	owner := &Service{Store: svc.Store, Machines: svc.Machines, Reporter: ownerReporter, DataDir: svc.DataDir, logf: func(string, ...any) {}}
	owner.NewExecutor = svc.NewExecutor
	owner.Start(t.Context())
	defer owner.Stop()
	if _, err := owner.Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	<-ownerReporter.finished
	log, err = os.ReadFile(job.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "before the restart"); n != 1 {
		t.Fatalf("the line appears %d times after the restart, want once:\n%s", n, log)
	}
	final, err := owner.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.LogBytes != int64(len(log)) {
		t.Fatalf("final offset %d, want the %d bytes of the log", final.LogBytes, len(log))
	}
}

// storedExec runs one statement against the job table.
func storedExec(svc *Service, query string, args ...any) error {
	db, err := svc.Store.db()
	if err != nil {
		return err
	}
	_, err = db.Exec(query, args...)
	return err
}

// TestOwnerClaimsWhatAStandbyQueuedLater is the fail-first test for review
// finding 1 of round 3: the owner already runs when a standby engine queues
// a job, and the owner claims it without a restart, through its claim
// interval. The tick channel stands in for the interval.
func TestOwnerClaimsWhatAStandbyQueuedLater(t *testing.T) {
	owner, _, ownerReporter, _ := newIdleService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "ran")
		return nil
	})
	tick := make(chan time.Time)
	owner.after = func(time.Duration) <-chan time.Time { return tick }
	owner.Start(t.Context())
	defer owner.Stop()
	standby, _, _, _ := newIdleService(t, blockingJob("never runs here"))
	standby.Store = owner.Store
	standby.Machines = owner.Machines
	standby.NewExecutor = owner.NewExecutor
	if _, err := standby.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	standby.Stop()
	tick <- time.Now()
	done := <-ownerReporter.finished
	if done.State != StatePassed {
		t.Fatalf("the owner ran the standby-queued job to %s, want passed", done.State)
	}
}

// TestStandbyCancelStopsTheOwnersJob is the fail-first test for review
// finding 3 of round 3: a standby engine cancels a job the owner runs. The
// node process stops, the row keeps its cancelled end, and the finished
// event is reported once.
func TestStandbyCancelStopsTheOwnersJob(t *testing.T) {
	// The job blocks until it is stopped, and its folder cleanup is the last
	// node call of a finished run, so the test can see the run ended.
	rmDone := make(chan struct{})
	running := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			close(rmDone)
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "working")
		<-ctx.Done()
		return ctx.Err()
	}
	owner, fake, ownerReporter, _ := newIdleService(t, running)
	tick := make(chan time.Time)
	owner.after = func(time.Duration) <-chan time.Time { return tick }
	owner.Start(t.Context())
	defer owner.Stop()
	standby, _, _, _ := newIdleService(t, blockingJob("never runs here"))
	standby.Store = owner.Store
	standby.Machines = owner.Machines
	standby.NewExecutor = owner.NewExecutor
	job, err := standby.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	standby.Stop()
	tick <- time.Now()
	if started := <-ownerReporter.started; started.ID != job.ID {
		t.Fatalf("started job %s, want %s", started.ID, job.ID)
	}
	standby2, _, standby2Reporter, _ := newIdleService(t, blockingJob("never runs here"))
	standby2.Store = owner.Store
	standby2.Machines = owner.Machines
	standby2.NewExecutor = owner.NewExecutor
	defer standby2.Stop()
	if _, err := standby2.Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	// The canceller reports the end once, as the one whose session submitted.
	if event := <-standby2Reporter.finished; event.State != StateCancelled {
		t.Fatalf("the canceller reported %s, want cancelled", event.State)
	}
	tick <- time.Now()
	<-rmDone
	// The owner stopped the node process and recorded the cancelled end
	// without a second report.
	select {
	case event := <-ownerReporter.finished:
		t.Fatalf("the owner reported %s too; the end is reported once", event.State)
	default:
	}
	if fake.StopCount() == 0 {
		t.Fatal("the node never saw a stop call")
	}
	row, err := owner.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateCancelled {
		t.Fatalf("the stored row is %s, want cancelled", row.State)
	}
}

// TestACancelBetweenClaimAndStartKeepsTheRowCancelled is the fail-first test
// for review finding 1 of round 4: a standby cancels after the owner claims
// a queued job but before the owner records the start. The row keeps its
// cancelled end and the node never sees the job. The run hook places the
// cancel exactly between the claim and the started write.
func TestACancelBetweenClaimAndStartKeepsTheRowCancelled(t *testing.T) {
	// The node run of the job is the old, racy behaviour: the unit starts and
	// the job prints one line. The fixed behaviour abandons the run instead.
	ranOnNode := make(chan struct{})
	run := func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		close(ranOnNode)
		fmt.Fprintln(out, "ran")
		return nil
	}
	owner, fake, ownerReporter, _ := newIdleService(t, run)
	cancelled := make(chan struct{})
	abandoned := make(chan struct{})
	owner.onRun = func(job Job) {
		// The run hook places the cancel exactly between the claim and the
		// write that records the start.
		standby, _, _, _ := newIdleService(t, run)
		standby.Store = owner.Store
		standby.Machines = owner.Machines
		standby.NewExecutor = owner.NewExecutor
		if _, err := standby.Cancel(t.Context(), job.ID); err != nil {
			t.Errorf("cancel: %v", err)
		}
		standby.Stop()
		close(cancelled)
	}
	owner.onAbandoned = func(Job) { close(abandoned) }
	owner.Start(t.Context())
	job, err := owner.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-abandoned:
	case <-ranOnNode:
		t.Fatal("the node started the job although the cancel had won the row")
	}
	owner.Stop()
	if n := len(fake.StartCalls()); n != 0 {
		t.Fatalf("the node started %d job(s) after the cancel", n)
	}
	select {
	case event := <-ownerReporter.finished:
		t.Fatalf("a never-started job reported %s", event.State)
	default:
	}
	row, err := owner.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateCancelled {
		t.Fatalf("the stored row is %s, want cancelled", row.State)
	}
}

// TestATerminalRowKeepsItsEnd is the fail-first test for review finding 1 of
// round 4 on the finish side: a write that reaches the table after the row
// already ended never overwrites the row, whatever state it carries.
func TestATerminalRowKeepsItsEnd(t *testing.T) {
	store := Store{DataDir: t.TempDir()}
	job := Job{ID: "job_cancel_race", RoomID: "dyna", Node: "here", Command: []string{"true"}, State: StateRunning, LogPath: filepath.Join(t.TempDir(), "log")}
	if err := store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	// Another engine ends the row while this one still holds its copy.
	job.State = StateCancelled
	if err := store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	// The stale engine writes its own end; the row keeps the cancelled one.
	late := job
	late.State = StatePassed
	row, won, err := store.SaveOpen(t.Context(), late)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("a write over a terminal row won")
	}
	if row.State != StateCancelled {
		t.Fatalf("the row is %s, want cancelled", row.State)
	}
	// A write on an open row still goes through.
	open := Job{ID: "job_open_race", RoomID: "dyna", Node: "here", Command: []string{"true"}, State: StateRunning, LogPath: filepath.Join(t.TempDir(), "log2")}
	if err := store.Save(t.Context(), open); err != nil {
		t.Fatal(err)
	}
	open.State = StatePassed
	row, won, err = store.SaveOpen(t.Context(), open)
	if err != nil {
		t.Fatal(err)
	}
	if !won || row.State != StatePassed {
		t.Fatalf("the open-row write won=%v and read %s", won, row.State)
	}
}

// TestACancelAfterTheStartKeepsTheRunsRecord is the fail-first test for the
// round-5 review finding: a standby reads a queued job, the owner records
// the start, and only then does the standby commit its cancellation. The
// cancellation changes only its own fields: the start time, folder and unit
// the run recorded stay, so the row still reads as started and the finish
// event is not suppressed.
func TestACancelAfterTheStartKeepsTheRunsRecord(t *testing.T) {
	store := Store{DataDir: t.TempDir()}
	submitted := time.Now().Truncate(time.Millisecond)
	job := Job{ID: "job_late_cancel", RoomID: "dyna", Node: "here", Command: []string{"true"}, State: StateQueued, Created: submitted, LogPath: filepath.Join(t.TempDir(), "log")}
	if err := store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	// The stale engine keeps this queued copy aside.
	stale, err := store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The owner records the start while the stale copy sits aside.
	started := submitted.Add(time.Second)
	running := stale
	running.State = StateRunning
	running.Started = started
	running.Dir = "/var/tmp/dyna-local-ci-1000/here/jobs/job_late_cancel"
	running.Unit = "dyna-job-job_late_cancel.service"
	if _, won, err := store.SaveOpen(t.Context(), running); err != nil || !won {
		t.Fatalf("record the start: won=%v err=%v", won, err)
	}
	// The stale engine cancels after that start.
	ended := started.Add(time.Minute)
	row, won, err := store.Cancel(t.Context(), job.ID, ended)
	if err != nil {
		t.Fatal(err)
	}
	if !won {
		t.Fatal("the cancellation of an open row lost")
	}
	if row.State != StateCancelled {
		t.Fatalf("the row is %s, want cancelled", row.State)
	}
	if row.Started.IsZero() {
		t.Fatal("the cancellation erased the recorded start, so the finish event would be suppressed")
	}
	if row.Dir == "" || row.Unit == "" {
		t.Fatal("the cancellation erased the job folder and unit")
	}
	if !row.Ended.Equal(ended) {
		t.Fatalf("the row ended at %v, want %v", row.Ended, ended)
	}
	stored, err := store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != StateCancelled || stored.Started.IsZero() || stored.Dir == "" || stored.Unit == "" {
		t.Fatalf("the stored row lost its run record: state=%s started=%v dir=%q unit=%q", stored.State, stored.Started, stored.Dir, stored.Unit)
	}
}
