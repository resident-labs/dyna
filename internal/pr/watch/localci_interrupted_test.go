package watch

import (
	"strings"
	"testing"

	"dyna-agent/internal/localci"
)

// stoppedRun is the result shape D-146 reported: every check of a trial on one
// machine stopped with budget cause engine-stop or cancelled, and no duration,
// because the engine restarted while the trial ran.
func stoppedRun() localci.Result {
	return localci.Result{
		Repo: "repo", SHA: strings.Repeat("a", 40), Node: "pve", Status: localci.OutcomeError,
		Cause: localci.CauseEngineStop, Error: "local CI stopped because the engine is restarting",
		Checks: []localci.SuiteResult{
			{Suite: "fmt", CheckName: "fmt", Node: "pve", Status: localci.OutcomeError, Cause: localci.CauseEngineStop, Error: "check stopped before its reports were read"},
			{Suite: "test-core", CheckName: "test-core", Node: "pve", Status: localci.OutcomeError, Cause: localci.CauseCancelled, Error: "context canceled"},
		},
	}
}

// A local run that Dyna itself stopped says nothing about the commit, so it runs
// again on the same machine, and the marker bounds that to one retry.
func TestStoppedLocalRunIsRetriedOnce(t *testing.T) {
	stopped := stoppedRun()
	if act := savedResultActionFor(stopped); act != retryStoppedRun {
		t.Fatalf("action = %v, want retryStoppedRun", act)
	}
	stopped.Resumed = true
	if act := savedResultActionFor(stopped); act != settleSavedResult {
		t.Fatalf("second interruption action = %v, want settleSavedResult", act)
	}
	// A machine failure keeps its own rule: it moves to another machine.
	machine := stoppedRun()
	machine.Cause = localci.CauseContainerFailure
	for i := range machine.Checks {
		machine.Checks[i].Cause = localci.CauseContainerFailure
	}
	if act := savedResultActionFor(machine); act != retryOnAnotherMachine {
		t.Fatalf("machine failure action = %v, want retryOnAnotherMachine", act)
	}
	// A test failure is evidence about the commit and never runs again.
	failed := stoppedRun()
	failed.Status = localci.OutcomeFailed
	failed.Cause = localci.CauseTestFailure
	failed.Checks = []localci.SuiteResult{{CheckName: "fmt", Status: localci.OutcomeFailed, Cause: localci.CauseTestFailure}}
	if act := savedResultActionFor(failed); act != settleSavedResult {
		t.Fatalf("test failure action = %v, want settleSavedResult", act)
	}
}

// A room with no hosted CI has no fallback, so a trial whose checks could not run
// fails with the typed cause instead of waiting for a provider that does not
// exist. That is what D-146 saw as "waiting for hosted CI fallback" for ever.
func TestLocalOnlyTrialFailsWhenItsChecksCouldNotRun(t *testing.T) {
	definition := watchDynaDefinition(t)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: strings.Repeat("a", 40), Branch: "dyna/merge-trial/1", Watching: true, State: "open"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	s := &Service{}

	// Dyna stopped the run and no replacement has started yet: the retry is
	// pending, so the trial waits with a truthful reason.
	stopped := stoppedRun()
	obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, true, true)
	if obs.Status != VerdictPending {
		t.Fatalf("stopped run = %s, want pending until the retry starts", obs.Status)
	}
	if strings.Contains(obs.Reason, "hosted") {
		t.Fatalf("retry reason names a hosted fallback that does not exist: %q", obs.Reason)
	}

	// The replacement was stopped too, or no retry is possible: the trial fails
	// and names the cause.
	stopped.Resumed = true
	obs = s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, true, true)
	if obs.Status != VerdictFailed {
		t.Fatalf("settled interruption = %s, want failed", obs.Status)
	}
	if !strings.Contains(obs.Reason, "local CI could not run its checks on pve") || !strings.Contains(obs.Reason, "engine") {
		t.Fatalf("failure reason does not name the node and cause: %q", obs.Reason)
	}
	if len(obs.Jobs) == 0 {
		t.Fatal("failure names no check that did not run")
	}

	// A room with a hosted provider still falls back, so the same result waits.
	if obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, true, false); obs.Status != VerdictPending {
		t.Fatalf("hosted room = %s, want it to wait for the fallback", obs.Status)
	}
	// A pull request head is never failed by a local run it does not have.
	if obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, false, true); obs.Status == VerdictFailed {
		t.Fatalf("pull request head = %s, want no local failure", obs.Status)
	}
}
