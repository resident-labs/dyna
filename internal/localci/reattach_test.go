package localci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reattachFixture runs one go-static check on a fake machine. The check stays
// running until release closes, so a test can stop the engine in the middle.
type reattachFixture struct {
	runner  Runner
	machine *fakeMachine
	request Request
	started chan struct{}
	release chan struct{}
	setups  atomic.Int32
}

func newReattachFixture(t *testing.T) *reattachFixture {
	t.Helper()
	checks, _, err := nodeChecks(testDynaDefinition(t), []Suite{SuiteGoStatic}, true)
	if err != nil {
		t.Fatal(err)
	}
	f := &reattachFixture{started: make(chan struct{}, 4), release: make(chan struct{})}
	f.runner = Runner{DataDir: t.TempDir(), Node: &Node{Name: "pve", SSH: "pip@pve", Slots: 1, Enabled: true}}
	f.machine = newFakeMachine(t, func(ctx context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) || fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "df -Pk"):
			fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
			fmt.Fprintln(out, "/dev/x 1048576 1 104857600 1% /var/tmp")
		case strings.Contains(script, "init.lock"):
			f.setups.Add(1)
		case strings.Contains(script, "tar -C"):
			return fakeNodeReports(out, checks, false)
		case name == "systemd-run":
			f.started <- struct{}{}
			select {
			case <-f.release:
				fmt.Fprintln(out, "ok")
				fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	f.machine.attach(&f.runner)
	f.request = Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{SuiteGoStatic}, MaxTest: time.Second, DeferSave: true}
	return f
}

// stopEngineDuringRun starts a run, waits until its check runs on the machine,
// and stops the engine under it.
func (f *reattachFixture) stopEngineDuringRun(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		result, _ := f.runner.Run(ctx, f.request)
		done <- result
	}()
	<-f.started
	previous := EngineStopping()
	MarkEngineStopping()
	t.Cleanup(func() { engineStopping.Store(previous) })
	cancel()
	if result := <-done; result.Cause != CauseEngineStop {
		t.Fatalf("stopped run cause = %s, want %s", result.Cause, CauseEngineStop)
	}
	ClearEngineStopping()
	if f.machine.stopCount() != 0 {
		t.Fatal("an engine stop stopped the check on the machine")
	}
}

// A check that ran through an engine restart keeps its result: the next engine
// follows the same check, does not start it again, and reads its reports (D-238).
func TestRestartedEngineReattachesToARunningCheck(t *testing.T) {
	f := newReattachFixture(t)
	f.stopEngineDuringRun(t)
	// The check finishes while no engine watches it.
	close(f.release)
	result, err := f.runner.Run(t.Context(), f.request)
	if err != nil {
		t.Fatalf("reattached run failed: %v", err)
	}
	if result.Status != OutcomePassed || len(result.Checks) != 1 || result.Checks[0].Status != OutcomePassed {
		t.Fatalf("result = %+v, want the check's own pass", result)
	}
	if got := len(f.machine.startCalls()); got != 1 {
		t.Fatalf("check started %d times, want once", got)
	}
	if got := f.setups.Load(); got != 1 {
		t.Fatalf("node setup ran %d times, want once", got)
	}
	if !strings.Contains(result.Checks[0].LogTail, "DYNA_CI_CHECK_EXIT=0") {
		t.Fatalf("reattached check lost its log: %q", result.Checks[0].LogTail)
	}
}

// A machine that lost the check, such as after a reboot, starts the run afresh.
func TestRestartedEngineStartsAgainWhenTheCheckIsGone(t *testing.T) {
	f := newReattachFixture(t)
	f.stopEngineDuringRun(t)
	f.machine.vanishAll()
	close(f.release)
	result, err := f.runner.Run(t.Context(), f.request)
	if err != nil || result.Status != OutcomePassed {
		t.Fatalf("fresh run = %s, %v", result.Status, err)
	}
	if got := len(f.machine.startCalls()); got != 2 {
		t.Fatalf("check started %d times, want a new start", got)
	}
}

// A second run of the same commit on the same machine, while the first is live,
// must not take over the first run's record or touch its workspace.
func TestConcurrentRunOfTheSameCommitLeavesTheLiveRunAlone(t *testing.T) {
	f := newReattachFixture(t)
	done := make(chan Result, 2)
	for range 2 {
		go func() {
			result, _ := f.runner.Run(t.Context(), f.request)
			done <- result
		}()
	}
	<-f.started
	<-f.started
	store := Store{DataDir: f.runner.DataDir}
	key, err := canonicalKey(f.request.Repo)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.attachmentRows(t.Context(), key, strings.Repeat("a", 40))
	if err != nil || len(rows) != 2 || len(rows[0].Checks) != 1 || len(rows[1].Checks) != 1 {
		t.Fatalf("records = %+v, %v, want one untouched record for each live run", rows, err)
	}
	if rows[0].RunID == rows[1].RunID || rows[0].orphaned() || rows[1].orphaned() {
		t.Fatalf("the runs share a record or lost their owner: %+v", rows)
	}
	if got := f.machine.stopCount(); got != 0 {
		t.Fatalf("the second run stopped %d checks of the first", got)
	}
	close(f.release)
	for range 2 {
		if result := <-done; result.Status != OutcomePassed {
			t.Fatalf("result = %+v", result)
		}
	}
}

// A recorded check that was never launched is started afresh in the resumed
// run, and a recorded check that vanished makes the run unusable: every
// recorded unit is stopped before the folder is removed.
func TestResumeRunHandlesUnlaunchedAndVanishedChecks(t *testing.T) {
	checks, _, err := nodeChecks(testDynaDefinition(t), []Suite{SuiteGoStatic, SuiteRelay}, true)
	if err != nil || len(checks) < 2 {
		t.Fatalf("checks = %d, %v", len(checks), err)
	}
	var scripts []string
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "pve", SSH: "pip@pve", Slots: 2, Enabled: true}}
	machine := newFakeMachine(t, func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		if script, _, ok := decodeNodeScript(name, args); ok && (strings.Contains(script, "worktree prune") || strings.Contains(script, "rm -rf --one-file-system")) {
			scripts = append(scripts, script)
		}
		return nil
	})
	machine.attach(&runner)
	store := Store{DataDir: runner.DataDir}
	repo, sha := "/repo", strings.Repeat("a", 40)
	runDir := cacheBase("pve") + "/runs/r1"
	first, second := checks[0].id, checks[1].id
	spec := func(id Suite) detachSpec {
		return detachSpec{ID: id, Dir: detachedDir(runDir, id), Unit: checkUnit("r1", id), Script: "true"}
	}
	if err := store.saveAttachment(t.Context(), repo, sha, "pve", attachment{RunID: "r1", Checks: []Suite{first, second}}); err != nil {
		t.Fatal(err)
	}
	// The run's own process is alive, so no other run may take its record.
	if att, err := runner.resumeRun(t.Context(), store, repo, sha, "/cache", checks); err != nil || att != nil {
		t.Fatalf("a live run's record was taken: %+v, %v", att, err)
	}
	if err := store.releaseAttachment(t.Context(), repo, sha, "pve", "r1"); err != nil {
		t.Fatal(err)
	}
	// Only the second check was launched.
	if err := runner.startDetached(t.Context(), spec(second)); err != nil {
		t.Fatal(err)
	}
	att, err := runner.resumeRun(t.Context(), store, repo, sha, "/cache", checks)
	if err != nil || att == nil || len(att.Checks) != 1 || att.Checks[0] != second {
		t.Fatalf("resume = %+v, %v, want only the launched check", att, err)
	}
	// The first resumed run ends without finishing, and the launched check
	// vanishes, as after a reboot.
	if err := store.releaseAttachment(t.Context(), repo, sha, "pve", "r1"); err != nil {
		t.Fatal(err)
	}
	machine.vanish(spec(second).Dir)
	att, err = runner.resumeRun(t.Context(), store, repo, sha, "/cache", checks)
	if err != nil || att != nil {
		t.Fatalf("resume = %+v, %v, want a fresh run", att, err)
	}
	// Teardown of every recorded check comes first, then the folder goes.
	if len(scripts) != 2 || !strings.Contains(scripts[0], spec(first).Dir) || !strings.Contains(scripts[0], spec(second).Dir) || !strings.Contains(scripts[1], "rm -rf --one-file-system") {
		t.Fatalf("cleanup = %q, want every recorded unit and scope stopped before the folder goes", scripts)
	}
	if rows, _ := store.attachmentRows(t.Context(), repo, sha); len(rows) != 0 {
		t.Fatalf("the unusable record was kept: %+v", rows)
	}
}

// The scheduler asks which machine holds a commit's checks.
func TestAttachedNodeNamesTheMachineThatHoldsTheChecks(t *testing.T) {
	store := Store{DataDir: t.TempDir()}
	repo, sha := "/repo", strings.Repeat("a", 40)
	if node, err := store.AttachedNode(t.Context(), repo, sha); err != nil || node != "" {
		t.Fatalf("before any record: %q %v", node, err)
	}
	if err := store.saveAttachment(t.Context(), repo, sha, "pve", attachment{RunID: "r1", Checks: []Suite{SuiteGoStatic}}); err != nil {
		t.Fatal(err)
	}
	// A record whose run is alive is not a candidate: that run is executing.
	if node, err := store.AttachedNode(t.Context(), repo, sha); err != nil || node != "" {
		t.Fatalf("with a live run: %q %v", node, err)
	}
	if err := store.releaseAttachment(t.Context(), repo, sha, "pve", "r1"); err != nil {
		t.Fatal(err)
	}
	if node, err := store.AttachedNode(t.Context(), repo, sha); err != nil || node != "pve" {
		t.Fatalf("with an orphaned record: %q %v", node, err)
	}
}

// Another process, such as a dyna git ci run command, may share the database with
// the engine. A record is taken over only when the process that owns it is gone,
// and only one process wins it.
func TestAttachmentIsTakenOnlyFromADeadOwner(t *testing.T) {
	previousOwner, previousAlive := attachmentOwner, ownerAlive
	t.Cleanup(func() { attachmentOwner, ownerAlive = previousOwner, previousAlive })
	live := map[int]bool{11: true, 22: true}
	ownerAlive = func(pid int, _ uint64) bool { return live[pid] }
	store := Store{DataDir: t.TempDir()}
	repo, sha := "/repo", strings.Repeat("a", 40)
	attachmentOwner = func() (int, uint64) { return 11, 1 }
	if err := store.saveAttachment(t.Context(), repo, sha, "pve", attachment{RunID: "r1", Checks: []Suite{SuiteGoStatic}}); err != nil {
		t.Fatal(err)
	}
	attachmentOwner = func() (int, uint64) { return 22, 1 }
	if _, err := store.claimAttachment(t.Context(), repo, sha, "pve"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a command took the record of a live engine: %v", err)
	}
	live[11] = false
	att, err := store.claimAttachment(t.Context(), repo, sha, "pve")
	if err != nil || att.RunID != "r1" {
		t.Fatalf("claim after the engine died: %+v, %v", att, err)
	}
	// A third process finds the record owned by a live one.
	attachmentOwner = func() (int, uint64) { return 33, 1 }
	live[33] = true
	if _, err := store.claimAttachment(t.Context(), repo, sha, "pve"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a record was taken twice: %v", err)
	}
}

// A teardown that fails leaves the folder and the record in place, so the
// survivors of the old run are not orphaned.
func TestResumeRunKeepsTheRecordWhenTeardownFails(t *testing.T) {
	checks, _, err := nodeChecks(testDynaDefinition(t), []Suite{SuiteGoStatic}, true)
	if err != nil {
		t.Fatal(err)
	}
	var removals int
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "pve", SSH: "pip@pve", Slots: 1, Enabled: true}}
	machine := newFakeMachine(t, func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		script, _, _ := decodeNodeScript(name, args)
		switch {
		case strings.Contains(script, "worktree prune"):
			return exitError(t, 69)
		case strings.Contains(script, "rm -rf --one-file-system"):
			removals++
		}
		return nil
	})
	machine.attach(&runner)
	store := Store{DataDir: runner.DataDir}
	repo, sha := "/repo", strings.Repeat("a", 40)
	if err := store.saveAttachment(t.Context(), repo, sha, "pve", attachment{RunID: "r1", Checks: []Suite{checks[0].id}}); err != nil {
		t.Fatal(err)
	}
	if err := store.releaseAttachment(t.Context(), repo, sha, "pve", "r1"); err != nil {
		t.Fatal(err)
	}
	// The recorded check has no unit on the machine, so the run is unusable.
	machine.vanish("none")
	if _, err := runner.resumeRun(t.Context(), store, repo, sha, "/cache", checks); err == nil {
		t.Fatal("a failed teardown was not reported")
	}
	if removals != 0 {
		t.Fatal("the folder was removed although the teardown failed")
	}
	if node, _ := store.AttachedNode(t.Context(), repo, sha); node != "pve" {
		t.Fatal("the record was dropped although the teardown failed")
	}
}

// A run that fails before it reaches its checks must not leave the record it
// took over owned by the live engine, or no later run could take it.
func TestEarlyFailureLeavesTheRecordForTheNextRun(t *testing.T) {
	f := newReattachFixture(t)
	f.stopEngineDuringRun(t)
	// The next run cannot make its report folder.
	results := filepath.Join(f.runner.DataDir, "local-ci")
	if err := os.RemoveAll(results); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(results, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Run(t.Context(), f.request); err == nil {
		t.Fatal("the run did not fail")
	}
	store := Store{DataDir: f.runner.DataDir}
	key, _ := canonicalKey(f.request.Repo)
	rows, err := store.attachmentRows(t.Context(), key, strings.Repeat("a", 40))
	if err != nil || len(rows) != 1 || !rows[0].orphaned() {
		t.Fatalf("records = %+v, %v, want the one record still free to take", rows, err)
	}
}
