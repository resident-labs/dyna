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
