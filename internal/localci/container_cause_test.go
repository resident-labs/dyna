package localci

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// A machine that cannot start a container has not tested the commit. The result
// must say so with a typed infrastructure cause, because a failed test blames
// the pull request and nothing ever retries it on another machine.
func TestContainerThatNeverStartedIsAnInfrastructureFailure(t *testing.T) {
	definition := parseRepositoryDefinition(t, false)
	checks, _, err := nodeChecks(definition, []Suite{"test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "arch", Slots: 1, Enabled: true}}
	legacy := func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "tar -C"):
			return fakeNodeReports(out, checks, false)
		case strings.Contains(script, "run --rm --init"):
			// The node's own trap follows the failed command: the check entered,
			// the exit status is 1, and the command inside the container never
			// started.
			fmt.Fprintln(out, "permission denied while trying to connect to the docker API at unix:///var/run/docker.sock")
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=1")
			fmt.Fprintln(out, "DYNA_CI_COMMAND_STARTED=0")
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{"test"}, MaxTest: time.Second, DeferSave: true})
	if err != nil {
		t.Fatalf("a machine failure is not a run error: %v", err)
	}
	if result.Status != OutcomeError {
		t.Fatalf("run status = %q, want %q", result.Status, OutcomeError)
	}
	if len(result.Checks) != 1 {
		t.Fatalf("checks = %+v", result.Checks)
	}
	check := result.Checks[0]
	if check.Status != OutcomeError || check.Cause != CauseContainerFailure {
		t.Fatalf("check = %+v, want an infrastructure error with cause %q", check, CauseContainerFailure)
	}
	if !strings.Contains(check.Error, "arch") {
		t.Fatalf("check error %q does not name the machine", check.Error)
	}
}

// The marker must not turn a real test failure into an infrastructure failure:
// a container that started the command and failed its tests still blames them.
func TestContainerThatStartedKeepsItsTestFailure(t *testing.T) {
	definition := parseRepositoryDefinition(t, false)
	checks, _, err := nodeChecks(definition, []Suite{"test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "hermeslab", Slots: 1, Enabled: true}}
	legacy := func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "tar -C"):
			return fakeNodeReports(out, checks, true)
		case strings.Contains(script, "run --rm --init"):
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=1")
			fmt.Fprintln(out, "DYNA_CI_COMMAND_STARTED=1")
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{"test"}, MaxTest: time.Second, DeferSave: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != OutcomeFailed {
		t.Fatalf("run status = %q, want %q", result.Status, OutcomeFailed)
	}
	if len(result.Checks) != 1 || result.Checks[0].Cause != CauseTestFailure {
		t.Fatalf("checks = %+v, want a test failure", result.Checks)
	}
}

// The checks decide the commit. A cleanup on the node that times out after every
// check passed is a warning on the run and never turns the result into an error.
func TestSlowNodeCleanupDoesNotChangeAPassedRun(t *testing.T) {
	definition := parseRepositoryDefinition(t, false)
	checks, _, err := nodeChecks(definition, []Suite{"test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "pve", Slots: 1, Enabled: true}}
	legacy := func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "no_container()"):
			// The cleanup runs out of its time.
			return context.DeadlineExceeded
		case strings.Contains(script, "tar -C"):
			return fakeNodeReports(out, checks, false)
		case strings.Contains(script, "run --rm --init"):
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
			fmt.Fprintln(out, "DYNA_CI_COMMAND_STARTED=1")
		}
		return nil
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{"test"}, MaxTest: time.Second, DeferSave: true})
	if err != nil {
		t.Fatalf("a slow cleanup failed the run: %v", err)
	}
	if result.Status != OutcomePassed {
		t.Fatalf("run status = %q, want %q", result.Status, OutcomePassed)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != NodeCleanupUnfinished {
		t.Fatalf("warnings = %v, want the unfinished cleanup", result.Warnings)
	}
}
