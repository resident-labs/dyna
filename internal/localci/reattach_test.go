package localci

import (
	"context"
	"fmt"
	"io"
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
	att, err := store.loadAttachment(t.Context(), key, strings.Repeat("a", 40), "pve")
	if err != nil || len(att.Checks) != 1 {
		t.Fatalf("the live run's record = %+v, %v", att, err)
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
	var removed []string
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "pve", SSH: "pip@pve", Slots: 2, Enabled: true}}
	machine := newFakeMachine(t, func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		if script, _, ok := decodeNodeScript(name, args); ok && strings.Contains(script, "rm -rf --one-file-system") {
			removed = append(removed, script)
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
	// Only the second check was launched.
	if err := runner.startDetached(t.Context(), spec(second)); err != nil {
		t.Fatal(err)
	}
	att, err := runner.resumeRun(t.Context(), store, repo, sha, checks)
	if err != nil || att == nil || len(att.Checks) != 1 || att.Checks[0] != second {
		t.Fatalf("resume = %+v, %v, want only the launched check", att, err)
	}
	// Now the launched check vanishes, as after a reboot.
	machine.vanish(spec(second).Dir)
	att, err = runner.resumeRun(t.Context(), store, repo, sha, checks)
	if err != nil || att != nil {
		t.Fatalf("resume = %+v, %v, want a fresh run", att, err)
	}
	if len(removed) != 1 || !strings.Contains(removed[0], spec(first).Dir) || !strings.Contains(removed[0], spec(second).Dir) || strings.Index(removed[0], detachedStopScript) > strings.Index(removed[0], "rm -rf --one-file-system") {
		t.Fatalf("cleanup = %q, want every recorded unit stopped before the folder goes", removed)
	}
	if _, err := store.loadAttachment(t.Context(), repo, sha, "pve"); err == nil {
		t.Fatal("the unusable record was kept")
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
	if node, err := store.AttachedNode(t.Context(), repo, sha); err != nil || node != "pve" {
		t.Fatalf("with a record: %q %v", node, err)
	}
}
