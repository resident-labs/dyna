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
	svc, _, reporter, _ := newService(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
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
