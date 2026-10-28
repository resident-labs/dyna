package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	// The folder is removed after the end is posted, so the test waits for the
	// removal itself, not for the end.
	removals := make(chan string, 2)
	svc.Start(t.Context())
	fake := jobstest.NewFake(t, jobstest.Protocol{Start: StartScript, Poll: PollScript, Stop: StopScript}, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		if !strings.Contains(joined, "rm -rf -- ") {
			fmt.Fprintln(out, "ran")
			return nil
		}
		if name == "ssh" {
			removals <- "remote"
		} else {
			removals <- "local"
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
	if where := <-removals; where != "remote" {
		t.Fatalf("the job's folder was removed on the %s machine, want on the node", where)
	}
}

// TestLogOffsetSurvivesEngineStop is the fail-first test for review finding
// 3: the row's stored log offset after an engine stop matches the log written
// so far, and the restarted engine never repeats output.
func TestLogOffsetSurvivesEngineStop(t *testing.T) {
	onNode := make(chan struct{})
	svc, _, _, _ := newService(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "before the restart")
		onNode <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	// The start event goes out before the node work, so wait for the node
	// itself: the stop below must land while the job runs.
	<-onNode
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
		x.Command = base
		x.Wait, x.Clock = fake.WaitFunc(), fake.ClockFunc()
		return x
	}
	svc.Start(t.Context())
	// The signal follows the write of the polled bytes, not the poll request:
	// a poll that has only been asked for proves nothing. Set before anything
	// can run: the service is idle until the submit below wakes it.
	if p := svc.poller(Conn{Name: "here"}); p != nil {
		p.applied = func(string) {
			select {
			case delivered <- struct{}{}:
			default:
			}
		}
	}
	job, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
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
// finding 3 of round 3 and the round-6 event order: a standby engine cancels
// a job the owner runs. The node process stops, the row keeps its cancelled
// end, and the owner alone reports the events, in order: one start, then one
// cancelled end. The canceller posts nothing.
func TestStandbyCancelStopsTheOwnersJob(t *testing.T) {
	// The job blocks until it is stopped, and its folder cleanup is the last
	// node call of a finished run, so the test can see the run ended.
	rmDone := make(chan struct{})
	onNode := make(chan struct{})
	running := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			close(rmDone)
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "working")
		onNode <- struct{}{}
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
	// The start event goes out before the node work, so wait for the node
	// itself: the cancel below must land while the job runs.
	<-onNode
	standby2, _, standby2Reporter, _ := newIdleService(t, blockingJob("never runs here"))
	standby2.Store = owner.Store
	standby2.Machines = owner.Machines
	standby2.NewExecutor = owner.NewExecutor
	defer standby2.Stop()
	if _, err := standby2.Cancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	// The canceller posts nothing; the owner owns the job's events.
	select {
	case event := <-standby2Reporter.finished:
		t.Fatalf("the canceller posted %s; the owner owns the events", event.State)
	default:
	}
	tick <- time.Now()
	<-rmDone
	// The owner stopped the node process and reports the end, once, after
	// the start it already posted.
	if event := <-ownerReporter.finished; event.State != StateCancelled {
		t.Fatalf("the owner reported %s, want cancelled", event.State)
	}
	select {
	case event := <-ownerReporter.finished:
		t.Fatalf("a second end %s arrived; the end is reported once", event.State)
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

// TestACancelDuringTheRunsPrepareEndsAfterItsStart is the fail-first test
// for the round-6 review, first interleaving, under the round-7 request
// design: a standby cancels after the owner posted the start, while the run
// still prepares. The node never sees the job; the owner alone posts the
// job's events, in order: the start, then the cancelled end. The prepare
// hook places the cancel exactly in that window.
func TestACancelDuringTheRunsPrepareEndsAfterItsStart(t *testing.T) {
	owner, fake, ownerReporter, _ := newIdleService(t, blockingJob("never runs"))
	cancelled := make(chan struct{})
	owner.onPrepared = func(job Job) {
		standby, _, standbyReporter, _ := newIdleService(t, blockingJob("never runs"))
		standby.Store = owner.Store
		standby.Machines = owner.Machines
		standby.NewExecutor = owner.NewExecutor
		if _, err := standby.Cancel(t.Context(), job.ID); err != nil {
			t.Errorf("cancel: %v", err)
		}
		standby.Stop()
		select {
		case event := <-standbyReporter.finished:
			t.Errorf("the canceller posted %s with no start posted", event.State)
		default:
		}
		close(cancelled)
	}
	owner.Start(t.Context())
	if _, err := owner.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	start := <-ownerReporter.started
	end := <-ownerReporter.finished
	owner.Stop()
	if start.ID != end.ID {
		t.Fatalf("start %s and end %s name different jobs", start.ID, end.ID)
	}
	if end.State != StateCancelled {
		t.Fatalf("the job ended %s, want cancelled", end.State)
	}
	select {
	case extra := <-ownerReporter.finished:
		t.Fatalf("a second end %s arrived; the end is reported once", extra.State)
	default:
	}
	if n := len(fake.StartCalls()); n != 0 {
		t.Fatalf("the node started %d job(s) after the cancel", n)
	}
	row, err := owner.Store.List(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(row) != 1 || row[0].State != StateCancelled {
		t.Fatalf("the jobs are %v, want one cancelled row", row)
	}
}

// TestACancelOfAQueuedJobStillReportsItsEnd is the fail-first test for the
// parent's rule that every submitted job ends with exactly one finished
// event: a job cancelled while still queued - the owner or another session
// may cancel it, not only the submitter - ends on the next sweep of the
// cancelling engine's loop, and that engine posts the job's only event.
// The node never sees the job, and the session that submitted it never
// polls.
func TestACancelOfAQueuedJobStillReportsItsEnd(t *testing.T) {
	standby, _, _, _ := newIdleService(t, blockingJob("never runs"))
	job, err := standby.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	// The job sits queued: no engine claimed it, so nothing started it.
	canceller, fake, cancellerReporter, _ := newIdleService(t, blockingJob("never runs"))
	canceller.Store = standby.Store
	canceller.Machines = standby.Machines
	canceller.NewExecutor = standby.NewExecutor
	tick := make(chan time.Time)
	canceller.after = func(time.Duration) <-chan time.Time { return tick }
	row, err := canceller.Cancel(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The request alone ends nothing: the row stays queued with the flag,
	// for the owning engine's next sweep.
	if row.State != StateQueued || !row.CancelRequested {
		t.Fatalf("the row is %s with request=%v, want queued with a standing request", row.State, row.CancelRequested)
	}
	canceller.Start(t.Context())
	defer canceller.Stop()
	// The loop's sweep ends the job: once, on the engine that took the row,
	// and that engine posts the job's only event.
	tick <- time.Now()
	event := <-cancellerReporter.finished
	if event.ID != job.ID || event.State != StateCancelled {
		t.Fatalf("the canceller posted %s of %s, want cancelled of %s", event.State, event.ID, job.ID)
	}
	select {
	case extra := <-cancellerReporter.finished:
		t.Fatalf("a second end %s arrived; the end is reported once", extra.State)
	default:
	}
	if n := len(fake.StartCalls()); n != 0 {
		t.Fatalf("the node started %d job(s), want none", n)
	}
	stored, err := canceller.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != StateCancelled || !stored.Started.IsZero() {
		t.Fatalf("the row is %s with started=%v, want cancelled with no start", stored.State, stored.Started)
	}
}

// TestAPrepareFailureAfterACancelEndsCancelledAfterItsStart is the
// fail-first test for the round-7 review, second finding: a standby cancels
// while the run prepares, and the preparation then fails before the node
// sees the job. The start went out with the durable transition, so the
// cancelled end follows it - no end ever precedes a start.
func TestAPrepareFailureAfterACancelEndsCancelledAfterItsStart(t *testing.T) {
	owner, _, ownerReporter, _ := newIdleService(t, blockingJob("never runs"))
	cancelled := make(chan struct{})
	owner.onPrepared = func(job Job) {
		standby, _, standbyReporter, _ := newIdleService(t, blockingJob("never runs"))
		standby.Store = owner.Store
		standby.Machines = owner.Machines
		standby.NewExecutor = owner.NewExecutor
		if _, err := standby.Cancel(t.Context(), job.ID); err != nil {
			t.Errorf("cancel: %v", err)
		}
		standby.Stop()
		select {
		case event := <-standbyReporter.finished:
			t.Errorf("the canceller posted %s; the owner owns the events", event.State)
		default:
		}
		// The preparation fails from here: the log path cannot be a file.
		if err := os.Mkdir(job.LogPath, 0o700); err != nil {
			t.Errorf("plant the blocking folder: %v", err)
		}
		close(cancelled)
	}
	owner.Start(t.Context())
	if _, err := owner.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	start := <-ownerReporter.started
	end := <-ownerReporter.finished
	if end.ID != start.ID {
		t.Fatalf("start %s and end %s name different jobs", start.ID, end.ID)
	}
	if end.State != StateCancelled {
		t.Fatalf("the job ended %s, want the cancelled end the request stands for", end.State)
	}
	row, err := owner.Store.Get(t.Context(), start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateCancelled {
		t.Fatalf("the row is %s, want cancelled", row.State)
	}
	owner.Stop()
}

// TestACancelledRequestSurvivesAnOwnerRestart is the fail-first test for
// the round-7 review, first finding: a standby cancels a running job and
// the owner exits before its tick stops the process. The request stands in
// the row, the next engine reattaches the still-running job, its tick stops
// the node process, and the room receives the job's end exactly once.
func TestACancelledRequestSurvivesAnOwnerRestart(t *testing.T) {
	onNode := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		onNode <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	owner, fake, _, _ := newIdleService(t, legacy)
	// The owner's own claim tick never fires: a nil channel parks its loop,
	// so no scheduling luck - a slow runner whose five seconds pass before
	// the next engine starts - can let the owner end the job itself. Only
	// the restarted engine carries the request to the end.
	owner.after = func(time.Duration) <-chan time.Time { return nil }
	owner.Start(t.Context())
	// The owner never stops: it dies with the job still running on the node.
	job, err := owner.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	<-onNode
	standby, _, standbyReporter, _ := newIdleService(t, legacy)
	standby.Store = owner.Store
	standby.Machines = owner.Machines
	standby.NewExecutor = owner.NewExecutor
	row, err := standby.Cancel(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	standby.Stop()
	if row.State != StateRunning || !row.CancelRequested {
		t.Fatalf("the row is %s with request=%v, want running with a standing request", row.State, row.CancelRequested)
	}
	select {
	case event := <-standbyReporter.finished:
		t.Fatalf("the canceller posted %s; the owner that runs the job owns the end", event.State)
	default:
	}
	// The next engine starts: it reattaches the running row and its claim
	// tick carries the standing request to the recorded end.
	next, _, nextReporter, _ := newIdleService(t, legacy)
	next.Store = owner.Store
	next.Machines = owner.Machines
	next.NewExecutor = owner.NewExecutor
	tick := make(chan time.Time)
	next.after = func(time.Duration) <-chan time.Time { return tick }
	next.Start(t.Context())
	defer next.Stop()
	tick <- time.Now()
	event := <-nextReporter.finished
	if event.ID != job.ID || event.State != StateCancelled {
		t.Fatalf("the restarted engine posted %s of %s, want cancelled of %s", event.State, event.ID, job.ID)
	}
	select {
	case extra := <-nextReporter.finished:
		t.Fatalf("a second end %s arrived; the end is reported once", extra.State)
	default:
	}
	if fake.StopCount() == 0 {
		t.Fatal("the node process was never stopped")
	}
	final, err := next.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StateCancelled || final.Started.IsZero() {
		t.Fatalf("the row is %s with started=%v, want cancelled with its start kept", final.State, final.Started)
	}
}

// TestAStaleSaveKeepsTheCancellationRequest is the fail-first test for the
// round-9 review, first finding: every whole-row write - a claimed job
// saving its start, a poll saving its offset - carries a stale copy, and
// none of them may drop a cancellation that landed in between: the upsert
// keeps the standing request of the row it overwrites.
func TestAStaleSaveKeepsTheCancellationRequest(t *testing.T) {
	store := Store{DataDir: t.TempDir()}
	job := Job{ID: "job_stale_save", RoomID: "dyna", Node: "here", Command: []string{"true"}, State: StateRunning, Created: time.Now(), LogPath: filepath.Join(t.TempDir(), "log")}
	if err := store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	// A stale copy from before the request saves over the row, twice: once
	// as the run started write, once as a poll offset write.
	stale := job
	stale.Note = "poll write"
	if _, _, err := store.SaveOpen(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	row, err := store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.CancelRequested {
		t.Fatal("a stale whole-row save cleared the standing cancellation request")
	}
	if row.Note != "poll write" {
		t.Fatalf("note = %q, want the stale save content", row.Note)
	}
}

// TestACancelBetweenTheClaimAndTheStartStillEndsTheJob covers the first
// finding service-level interleaving: a cancellation lands after the
// claim read the row but before the run records its start. The stale
// started write keeps the request, the run sees it before the node works,
// and the job ends cancelled, after its start, exactly once.
func TestACancelBetweenTheClaimAndTheStartStillEndsTheJob(t *testing.T) {
	owner, fake, ownerReporter, runStarted := newIdleService(t, blockingJob("never runs"))
	cancelled := make(chan struct{})
	owner.onRun = func(job Job) {
		runStarted <- job
		standby, _, standbyReporter, _ := newIdleService(t, blockingJob("never runs"))
		standby.Store = owner.Store
		standby.Machines = owner.Machines
		standby.NewExecutor = owner.NewExecutor
		if _, err := standby.Cancel(t.Context(), job.ID); err != nil {
			t.Errorf("cancel: %v", err)
		}
		standby.Stop()
		select {
		case event := <-standbyReporter.finished:
			t.Errorf("the canceller posted %s; the owner owns the events", event.State)
		default:
		}
		close(cancelled)
	}
	owner.Start(t.Context())
	if _, err := owner.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	start := <-ownerReporter.started
	end := <-ownerReporter.finished
	owner.Stop()
	if start.ID != end.ID {
		t.Fatalf("start %s and end %s name different jobs", start.ID, end.ID)
	}
	if end.State != StateCancelled {
		t.Fatalf("the job ended %s, want cancelled", end.State)
	}
	select {
	case extra := <-ownerReporter.finished:
		t.Fatalf("a second end %s arrived; the end is reported once", extra.State)
	default:
	}
	if n := len(fake.StartCalls()); n != 0 {
		t.Fatalf("the node started %d job(s), want none", n)
	}
}

// TestARestartAtAStartNeverPostedLaunchesAndPosts is the fail-first test
// for the round-9 review, second finding: an engine crashes after it saved
// the running row but before it posted the start or launched the unit. The
// next engine reads the row, posts the missing start, launches the job
// fresh, and reports its one end.
func TestARestartAtAStartNeverPostedLaunchesAndPosts(t *testing.T) {
	onNode := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "ran")
		onNode <- struct{}{}
		return nil
	}
	dead, fake, _, _ := newIdleService(t, legacy)
	dir := dead.DataDir
	started := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	job := Job{ID: "job_boundary", RoomID: "dyna", Node: "here", Command: []string{"echo", "hi"}, State: StateRunning, Started: started, Timeout: time.Minute, Created: started.Add(-time.Minute), LogPath: filepath.Join(dir, "jobs", "job_boundary", "log"), Dir: NodeBase("here") + "/jobs/job_boundary", Unit: "dyna-job-job_boundary.service"}
	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		t.Fatal(err)
	}
	if err := dead.Store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	// The row says running with no start posted: the crash boundary.
	next, _, nextReporter, _ := newIdleService(t, legacy)
	next.Store = dead.Store
	next.Machines = dead.Machines
	next.NewExecutor = dead.NewExecutor
	next.Start(t.Context())
	defer next.Stop()
	start := <-nextReporter.started
	if start.ID != job.ID {
		t.Fatalf("started %s, want %s", start.ID, job.ID)
	}
	<-onNode
	end := <-nextReporter.finished
	if end.State != StatePassed {
		t.Fatalf("the job ended %s, want passed", end.State)
	}
	if n := len(fake.StartCalls()); n != 1 {
		t.Fatalf("the node saw %d start(s), want one fresh launch", n)
	}
	row, err := next.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StatePassed || !row.StartPosted {
		t.Fatalf("the row is %s with start_posted=%v, want passed with the start recorded", row.State, row.StartPosted)
	}
}

// TestARestartBeforeTheLaunchRelaunchesTheJob is the fail-first test for
// the round-10 review, first finding: an engine crashes after it saved the
// start - start_posted is set - but before it launched the unit. The row
// says running, the node has no folder. The restarted engine attaches,
// learns the unit was never launched, and launches the job fresh: one
// launch, one passed end, and no second start event for a start that
// already went out.
func TestARestartBeforeTheLaunchRelaunchesTheJob(t *testing.T) {
	onNode := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "ran")
		onNode <- struct{}{}
		return nil
	}
	dead, fake, _, _ := newIdleService(t, legacy)
	dir := dead.DataDir
	started := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	job := Job{ID: "job_unlaunched", RoomID: "dyna", Node: "here", Command: []string{"echo", "hi"}, State: StateRunning, Started: started, StartPosted: true, Timeout: time.Minute, Created: started.Add(-time.Minute), LogPath: filepath.Join(dir, "jobs", "job_unlaunched", "log"), Dir: NodeBase("here") + "/jobs/job_unlaunched", Unit: "dyna-job-job_unlaunched.service"}
	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		t.Fatal(err)
	}
	if err := dead.Store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	next, _, nextReporter, _ := newIdleService(t, legacy)
	next.Store = dead.Store
	next.Machines = dead.Machines
	next.NewExecutor = dead.NewExecutor
	next.Start(t.Context())
	defer next.Stop()
	<-onNode
	end := <-nextReporter.finished
	if end.State != StatePassed {
		t.Fatalf("the job ended %s, want passed: a start recorded with no unit was never launched", end.State)
	}
	select {
	case start := <-nextReporter.started:
		t.Fatalf("a second start for %s arrived; the row already says the start went out", start.ID)
	default:
	}
	if n := len(fake.StartCalls()); n != 1 {
		t.Fatalf("the node saw %d launch(es), want one fresh launch", n)
	}
}

// TestARecoveredLaunchPushesItsCommit is the fail-first test for the
// round-10 review, second finding: a crash before the start was posted
// makes the restart launch fresh, and a fresh launch of a job pinned to a
// commit must push that commit to the node, or the job runs without its
// commit. The row recovers with its start, its push, and its passed end.
func TestARecoveredLaunchPushesItsCommit(t *testing.T) {
	onNode := make(chan struct{}, 1)
	var pushDest, pushRefspec string
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if name == "env" && len(args) > 5 && args[1] == "git" && args[2] == "push" {
			pushDest, pushRefspec = args[4], args[5]
			return nil
		}
		if len(args) > 0 && strings.Contains(args[len(args)-1], "update-ref -d ") {
			// The commit-ref drop before the push: node upkeep, not the job.
			return nil
		}
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "ran")
		onNode <- struct{}{}
		return nil
	}
	dead, _, _, _ := newIdleService(t, legacy)
	dir := dead.DataDir
	started := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	sha := "c0ffee0000000000000000000000000000000000"
	repo := filepath.Join(dir, "repo")
	job := Job{ID: "job_recover_push", RoomID: "dyna", Node: "here", Command: []string{"go", "test"}, Repo: repo, SHA: sha, State: StateRunning, Started: started, Timeout: time.Minute, Created: started.Add(-time.Minute), LogPath: filepath.Join(dir, "jobs", "job_recover_push", "log"), Dir: NodeBase("here") + "/jobs/job_recover_push", Unit: "dyna-job-job_recover_push.service"}
	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		t.Fatal(err)
	}
	if err := dead.Store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	next, _, nextReporter, _ := newIdleService(t, legacy)
	next.Store = dead.Store
	next.Machines = dead.Machines
	next.NewExecutor = dead.NewExecutor
	next.Start(t.Context())
	defer next.Stop()
	start := <-nextReporter.started
	if start.ID != job.ID {
		t.Fatalf("started %s, want %s", start.ID, job.ID)
	}
	<-onNode
	end := <-nextReporter.finished
	if end.State != StatePassed {
		t.Fatalf("the job ended %s, want passed", end.State)
	}
	if want := NodeBase("here") + "/" + RepoKey(repo) + ".git"; pushDest != want {
		t.Fatalf("the recovered launch pushed to %q, want the node cache %q", pushDest, want)
	}
	if want := sha + ":refs/dyna/ci/" + sha; pushRefspec != want {
		t.Fatalf("the recovered launch pushed %q, want %q", pushRefspec, want)
	}
}

// TestARestartInsideTheStartRelaunches is the fail-first test for the
// round-11 review: the engine or the node-side start dies after the start
// made the job folder but before the unit ran. The folder is there, the
// unit is not, so a poll reports gone and the job looks lost. The row
// records that the node never accepted the unit - launched stays unset -
// and the restarted engine relaunches the job instead: one launch, one
// passed end, no second start for a start that already went out.
func TestARestartInsideTheStartRelaunches(t *testing.T) {
	onNode := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.Contains(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "ran")
		onNode <- struct{}{}
		return nil
	}
	dead, fake, _, _ := newIdleService(t, legacy)
	dir := dead.DataDir
	started := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	job := Job{ID: "job_inside_start", RoomID: "dyna", Node: "here", Command: []string{"echo", "hi"}, State: StateRunning, Started: started, StartPosted: true, Timeout: time.Minute, Created: started.Add(-time.Minute), LogPath: filepath.Join(dir, "jobs", "job_inside_start", "log"), Dir: NodeBase("here") + "/jobs/job_inside_start", Unit: "dyna-job-job_inside_start.service"}
	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		t.Fatal(err)
	}
	if err := dead.Store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	// The interrupted start left a folder the unit never ran in.
	fake.Mkdir(path.Join(job.Dir, "detached"))
	next, _, nextReporter, _ := newIdleService(t, legacy)
	next.Store = dead.Store
	next.Machines = dead.Machines
	next.NewExecutor = dead.NewExecutor
	next.Start(t.Context())
	defer next.Stop()
	<-onNode
	end := <-nextReporter.finished
	if end.State != StatePassed {
		t.Fatalf("the job ended %s, want passed: a folder without a unit is an interrupted start, not a lost job", end.State)
	}
	select {
	case start := <-nextReporter.started:
		t.Fatalf("a second start for %s arrived; the row already says the start went out", start.ID)
	default:
	}
	if n := len(fake.StartCalls()); n != 1 {
		t.Fatalf("the node saw %d launch(es), want one fresh launch", n)
	}
	row, err := next.Store.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StatePassed || !row.Launched {
		t.Fatalf("the row is %s with launched=%v, want passed with the launch recorded", row.State, row.Launched)
	}
}

// TestARecoveryKeepsAUnitTheNodeAlreadyAccepted is the fail-first test
// for the round-12 review, first finding: the engine dies after the node
// accepted the unit but before it recorded the launch. The recovery must
// not stop that unit - its work already began, and stopping it to start
// again runs the side effects twice. The node's start is idempotent, so
// the recovered run starts nothing and follows the unit that is there.
func TestARecoveryKeepsAUnitTheNodeAlreadyAccepted(t *testing.T) {
	var runs atomic.Int32
	began := make(chan struct{}, 4)
	killed := make(chan struct{}, 1)
	release := make(chan struct{})
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.HasPrefix(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		runs.Add(1)
		select {
		case began <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			select {
			case killed <- struct{}{}:
			default:
			}
			return ctx.Err()
		case <-release:
			fmt.Fprintln(out, "accepted and finished")
			return nil
		}
	}
	owner, _, _, _ := newIdleService(t, legacy)
	owner.Start(t.Context())
	submitted, err := owner.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	<-began
	// The engine dies with the unit running on the node.
	SetEngineStoppingForTest(true)
	owner.Stop()
	SetEngineStoppingForTest(false)
	row, err := owner.Store.Get(t.Context(), submitted.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The next engine reads the row as the crash left it: the start went
	// out, the node accepted the unit, but the launch record never landed.
	next, _, nextReporter, _ := newIdleService(t, legacy)
	next.Store = Store{DataDir: t.TempDir()}
	next.Machines = owner.Machines
	next.NewExecutor = owner.NewExecutor
	crashed := row
	crashed.Launched = false
	if err := next.Store.Save(t.Context(), crashed); err != nil {
		t.Fatal(err)
	}
	// The recovery's start attempt is past every decision that could stop
	// the unit, on any version of the recovery: wait for it before the job
	// may finish. Set while the service is still idle.
	started := make(chan struct{}, 1)
	p := next.poller(Conn{Name: "here"})
	markLaunched := p.Launched
	p.Launched = func(spec RunSpec) {
		if markLaunched != nil {
			markLaunched(spec)
		}
		started <- struct{}{}
	}
	next.Start(t.Context())
	defer next.Stop()
	select {
	case <-started:
		close(release)
	case <-killed:
		// A recovery that stops the unit it was meant to keep: the end
		// that follows names the wrong it did.
	}
	end := <-nextReporter.finished
	if end.State != StatePassed {
		t.Fatalf("the job ended %s, want passed: the recovery must keep the unit the node already accepted", end.State)
	}
	select {
	case start := <-nextReporter.started:
		t.Fatalf("a second start for %s arrived; the row already says the start went out", start.ID)
	default:
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("the job script ran %d time(s), want once", n)
	}
	final, err := next.Store.Get(t.Context(), crashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StatePassed || !final.Launched {
		t.Fatalf("the row is %s with launched=%v, want passed with the launch recorded", final.State, final.Launched)
	}
}

// TestASharedPollerMarksEachLaunchedJob covers the round-12 review, second
// finding: one poller serves every job on a node, and its launch callback
// is one shared closure. Two jobs launched together must each mark their
// own row, not the row the last setter captured.
func TestASharedPollerMarksEachLaunchedJob(t *testing.T) {
	legacy := func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
		if len(args) > 0 && strings.HasPrefix(args[len(args)-1], "rm -rf -- ") {
			fmt.Fprintln(out, "removed")
			return nil
		}
		fmt.Fprintln(out, "ran")
		return nil
	}
	svc, _, reporter, _ := newService(t, legacy)
	first, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"false"}})
	if err != nil {
		t.Fatal(err)
	}
	ends := map[string]Job{}
	for range 2 {
		event := <-reporter.finished
		ends[event.ID] = event
	}
	for _, id := range []string{first.ID, second.ID} {
		row, err := svc.Store.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !row.Launched {
			t.Fatalf("job %s ended %s with launched=false; the shared callback marked the wrong row", id, row.State)
		}
		if row.State != ends[id].State {
			t.Fatalf("job %s row is %s, want the reported %s", id, row.State, ends[id].State)
		}
	}
}
