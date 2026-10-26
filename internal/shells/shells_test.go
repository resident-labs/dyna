package shells

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
)

func TestSuperviseRecordsOutputAndExitWithoutEventLog(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	meta, err := store.Create(StartOptions{
		Name: "probe",
		Cmd:  []string{"/bin/sh", "-c", "printf 'hello\\n'; exit 7"},
		Cwd:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	dir, err := store.dir(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil || !strings.Contains(string(output), "hello") {
		t.Fatalf("output = %q, %v", output, err)
	}
	exit, err := store.ReadExit(meta.ID)
	if err != nil || exit == nil || exit.Code != 7 {
		t.Fatalf("exit = %#v, %v", exit, err)
	}
	if m, err := store.ReadMeta(meta.ID); err != nil || m.OutputLines != 1 {
		t.Fatalf("lines = %d, %v", m.OutputLines, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("events.jsonl should not exist, stat err = %v", err)
	}
}

func runningShell(t *testing.T, s *Store, owner string, keep bool) Meta {
	t.Helper()
	meta, err := s.Create(StartOptions{Owner: owner, Keep: keep, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan int, 1)
	s.OnStarted = func(pid int) { started <- pid }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Supervise(ctx, meta.ID) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("supervisor: %v", err)
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not start")
	}
	in, err := s.Info(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != "running" || in.Pid == 0 {
		t.Fatalf("started shell = %s, pid %d", in.Status, in.Pid)
	}
	return in.Meta
}

func ownerRecord(t *testing.T, s *Store, owner string, closed bool) {
	t.Helper()
	meta := map[string]any{"id": owner}
	if closed {
		meta["closed"] = time.Now()
	}
	recordtest.Write(t, filepath.Join(filepath.Dir(s.Root), "sessions"), owner, meta)
}

func TestSweepStopsClosedAndMissingOwnersAndPreservesKeep(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	for i, tc := range []struct {
		name                           string
		closed, missing, keep, stopped bool
	}{
		{name: "open"},
		{name: "closed", closed: true, stopped: true},
		{name: "missing", missing: true, stopped: true},
		{name: "keep closed", closed: true, keep: true},
		{name: "keep missing", missing: true, keep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := fmt.Sprintf("ses_20261017-12000%d-abcdef01", i)
			ownerRecord(t, s, owner, false)
			meta := runningShell(t, s, owner, tc.keep)
			if tc.missing {
				recordtest.Remove(t, filepath.Join(filepath.Dir(s.Root), "sessions"), owner)
			} else {
				ownerRecord(t, s, owner, tc.closed)
			}
			if err := s.Sweep(); err != nil {
				t.Fatal(err)
			}
			in, err := s.Info(meta.ID)
			if err != nil || (in.Status == "exited") != tc.stopped {
				t.Fatalf("status = %s, want stopped=%v, error=%v", in.Status, tc.stopped, err)
			}
			if in.Keep != tc.keep {
				t.Fatalf("stored keep=%v, want %v", in.Keep, tc.keep)
			}
			if tc.stopped {
				output, err := s.Tail(meta.ID, 100)
				if err != nil || !strings.Contains(output, "closed or missing; stopping shell") {
					t.Fatalf("missing stop log: %q, %v", output, err)
				}
				if syscall.Kill(meta.Pid, 0) == nil {
					t.Fatal("stopped process still exists")
				}
			}
		})
	}
}

func TestStopOwnerOnlyStopsItsNonKeepShells(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	owner := "ses_20261017-120001-abcdef01"
	other := "ses_20261017-120002-abcdef01"
	ownerRecord(t, s, owner, false)
	ownerRecord(t, s, other, false)
	normal := runningShell(t, s, owner, false)
	kept := runningShell(t, s, owner, true)
	unrelated := runningShell(t, s, other, false)
	ownerRecord(t, s, owner, true)
	if err := s.StopOwner(owner); err != nil {
		t.Fatal(err)
	}
	for _, meta := range []Meta{normal, kept, unrelated} {
		in, err := s.Info(meta.ID)
		want := "running"
		if meta.ID == normal.ID {
			want = "exited"
		}
		if err != nil || in.Status != want {
			t.Fatalf("shell %s: status=%s, want %s, error=%v", meta.ID, in.Status, want, err)
		}
	}
}

// D-165: cancelling a workflow must stop the managed shell jobs its
// workers left behind, the way closing a session stops that session's shells.
func TestStopRunStopsItsNonKeepShells(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	owner := "ses_20261017-120004-abcdef01"
	ownerRecord(t, s, owner, false)
	start := func(opts StartOptions) Meta {
		t.Helper()
		meta, err := s.Create(opts)
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan int, 1)
		s.OnStarted = func(pid int) { started <- pid }
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Supervise(ctx, meta.ID) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("supervisor: %v", err)
			}
		})
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("shell did not start")
		}
		in, err := s.Info(meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		if in.Status != "running" || in.Pid == 0 {
			t.Fatalf("started shell = %s, pid %d", in.Status, in.Pid)
		}
		return in.Meta
	}
	run := "wf_20261026-010101-abcdef01"
	sleep := StartOptions{Owner: owner, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()}
	normal := start(StartOptions{Owner: owner, Run: run, Keep: false, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	kept := start(StartOptions{Owner: owner, Run: run, Keep: true, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	sameOwner := start(sleep)
	otherRun := start(StartOptions{Owner: owner, Run: "wf_20261026-020202-abcdef02", Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	if err := s.StopRun(run); err != nil {
		t.Fatal(err)
	}
	for _, meta := range []Meta{normal, kept, sameOwner, otherRun} {
		in, err := s.Info(meta.ID)
		want := "running"
		if meta.ID == normal.ID {
			want = "exited"
		}
		if err != nil || in.Status != want {
			t.Fatalf("shell %s: status=%s, want %s, error=%v", meta.ID, in.Status, want, err)
		}
	}
	output, err := s.Tail(normal.ID, 100)
	if err != nil || !strings.Contains(output, "run "+run+" is canceled; stopping shell") {
		t.Fatalf("missing stop log: %q, %v", output, err)
	}
}

// D-165: a supervisor delayed until its published job reads as lost must
// refuse to launch its worker's command behind a cancelled run, and must
// record the exit, so the job does not linger as open work.
func TestSuperviseRefusesALostShellAfterItsRunCancels(t *testing.T) {
	dataDir := storetest.Isolate(t)
	s := &Store{Root: filepath.Join(dataDir, "shells")}
	owner := "ses_20261017-120005-abcdef01"
	ownerRecord(t, s, owner, false) // open owner: only run cancellation refuses this job
	runID := "wf_20261026-010101-abcdef01"
	meta, err := s.Create(StartOptions{Owner: owner, Run: runID, Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Age the published row past the starting window, so a job whose supervisor
	// has not recorded itself reads as lost and the cancel snapshot skips it.
	meta.Started = time.Now().Add(-11 * time.Second)
	if err := s.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	in, err := s.Info(meta.ID)
	if err != nil || in.Status != "lost" {
		t.Fatalf("aged job = %s, %v; want lost", in.Status, err)
	}
	if list, err := s.List(false); err != nil || len(list) != 0 {
		t.Fatalf("cancel snapshot = %#v, %v; the lost job must be skipped", list, err)
	}
	runstoretest.Write(t, dataDir, runstore.Meta{ID: runID, Name: "late-supervisor", Status: "running", StartedAt: time.Now()})
	if err := runstore.Cancel(runID); err != nil {
		t.Fatal(err)
	}
	// The delayed supervisor finally runs.
	started := make(chan int, 1)
	s.OnStarted = func(pid int) { started <- pid }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Supervise(ctx, meta.ID) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("supervisor: %v", err)
			}
		default:
		}
	})
	select {
	case pid := <-started:
		t.Fatalf("the canceled run's command started as pid %d", pid)
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	}
	in, err = s.Info(meta.ID)
	if err != nil || in.Status != "exited" || in.Exit == nil || in.Exit.Error != "run is canceled" {
		t.Fatalf("job after the late supervisor = %#v, %v; want a run-canceled exit", in, err)
	}
	output, err := s.Tail(meta.ID, 100)
	if err != nil || !strings.Contains(output, "run is canceled") {
		t.Fatalf("missing refusal log: %q, %v", output, err)
	}
}

func TestSuperviseDoesNotLaunchAfterOwnerCloses(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	owner := "ses_20261017-120003-abcdef01"
	ownerRecord(t, s, owner, false)
	meta, err := s.Create(StartOptions{Owner: owner, Cmd: []string{"sh", "-c", "echo unexpected"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ownerRecord(t, s, owner, true)
	if err := s.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	in, err := s.Info(meta.ID)
	if err != nil || in.Status != "exited" || in.Pid != 0 {
		t.Fatalf("closed owner shell launched: %#v, %v", in, err)
	}
	output, err := s.Tail(meta.ID, 100)
	if err != nil || strings.Contains(output, "unexpected") || !strings.Contains(output, "stopped before start") {
		t.Fatalf("output = %q, %v", output, err)
	}
}

func TestSweepDoesNotStopShellWithUnreadableOwnerEvidence(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	owner := "ses_20261017-120004-abcdef01"
	ownerRecord(t, s, owner, false)
	meta := runningShell(t, s, owner, false)
	recordtest.WriteRaw(t, filepath.Join(filepath.Dir(s.Root), "sessions"), owner, []byte("broken JSON"))
	if err := s.Sweep(); err == nil {
		t.Fatal("malformed owner evidence was ignored")
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "running" {
		t.Fatalf("shell status = %s, %v", in.Status, err)
	}
}

func TestStopStillSignalsWhenLogIsMissing(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta := runningShell(t, s, "", false)
	path, err := s.OutputPath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	in, err := s.Stop(meta.ID, "owner")
	if err == nil || in.Status != "exited" {
		t.Fatalf("status = %s, error = %v; want exited and a log error", in.Status, err)
	}
	if syscall.Kill(meta.Pid, 0) == nil {
		t.Fatal("log failure prevented process shutdown")
	}
}

func TestStopWaitsForChildPID(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: t.TempDir()}
	meta, err := s.Create(StartOptions{Cmd: []string{"sleep", "600"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Hold the supervisor at the state before it publishes the child PID.
	meta.Supervisor = os.Getpid()
	if err := s.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	type stopResult struct {
		info Info
		err  error
	}
	stopped := make(chan stopResult, 1)
	go func() {
		in, err := s.Stop(meta.ID, "owner")
		stopped <- stopResult{in, err}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	supervised := make(chan error, 1)
	supervisorStarted, stopDone := false, false
	t.Cleanup(func() {
		cancel()
		if supervisorStarted {
			select {
			case err := <-supervised:
				if err != nil {
					t.Errorf("supervisor: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("supervisor cleanup did not finish")
			}
		}
		if !stopDone {
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Error("Stop cleanup did not finish")
			}
		}
	})
	select {
	case result := <-stopped:
		stopDone = true
		t.Fatalf("Stop returned before child startup: status=%s, error=%v", result.info.Status, result.err)
	case <-time.After(100 * time.Millisecond):
		// A negative assertion: Stop must stay blocked until startup is released.
	}
	supervisorStarted = true
	go func() { supervised <- s.Supervise(ctx, meta.ID) }()
	select {
	case result := <-stopped:
		stopDone = true
		if result.err != nil || result.info.Status != "exited" || result.info.Pid <= 0 {
			t.Fatalf("Stop result: %#v, %v", result.info, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not stop the child after startup")
	}
}

func TestShellPublicationClearsOwnerDormancy(t *testing.T) {
	root := t.TempDir()
	id := "ses_20261019-120000-abcdef12"
	recordtest.WriteRaw(t, filepath.Join(root, "sessions"), id, []byte(`{"id":"`+id+`","status":"dormant","finished":"2026-10-19T08:00:00Z"}`))
	s := &Store{Root: filepath.Join(root, "shells")}
	if _, err := s.Create(StartOptions{Owner: id, Cmd: []string{"true"}, Cwd: root}); err != nil {
		t.Fatal(err)
	}
	raw, err := recordtest.ReadRaw(t, filepath.Join(root, "sessions"), id)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status   string
		Finished time.Time
	}
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "idle" || !got.Finished.IsZero() {
		t.Fatalf("shell owner = %s", raw)
	}
}

func TestJobCommandKnowsItsJobID(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"/bin/sh", "-c", "echo job=$" + JobEnv}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := s.Tail(meta.ID, 5); err != nil || !strings.Contains(out, "job="+meta.ID) {
		t.Fatalf("output = %q, %v", out, err)
	}
}

func TestStopRecordsWhoStoppedTheJob(t *testing.T) {
	storetest.ClearStamp(t)
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta := runningShell(t, s, "", false)
	if _, err := s.Stop(meta.ID, "agent"); err != nil {
		t.Fatal(err)
	}
	if m, err := s.ReadMeta(meta.ID); err != nil || m.StopBy != "agent" {
		t.Fatalf("stopBy = %q, %v", m.StopBy, err)
	}
}
