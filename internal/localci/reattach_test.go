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
