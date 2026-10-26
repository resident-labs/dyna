package watch

import (
	"strings"
	"testing"
	"time"

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

// A trial whose saved run Dyna stopped is admitted again, but only once: the
// replacement carries the marker, and a busy fleet never spends that retry.
func TestStoppedTrialRetryHappensOnceAndWaitsForASlot(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	repo, err := localci.CanonicalRepo(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	head := record.Head
	stopped := stoppedRun()
	stopped.Repo, stopped.SHA = repo, head
	ci.results[head] = stopped
	ci.nodes = []localci.Node{{Name: "hermes", Slots: 1, Enabled: true}}
	job := registeredCIJob{record: record, head: head, branch: record.Branch, trial: true, wholeSuite: true}

	// The only machine is busy, so the retry cannot start and must not be spent.
	runs := e.s.nodeRuns()
	runs.Lock()
	runs.runs["another-head"] = &registeredCIRun{node: "hermes", slots: 1, head: "another", local: true, cancel: func() {}, stopped: make(chan struct{})}
	runs.Unlock()
	started, err := e.s.startRegisteredCI(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("a busy machine started the retry")
	}
	if ci.results[head].Resumed {
		t.Fatal("a retry that never started was spent")
	}

	// Free the slot: the retry starts, and the replacement is what carries the
	// marker.
	runs.Lock()
	delete(runs.runs, "another-head")
	runs.Unlock()
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	ci.saved = make(chan struct{}, 1)
	changed := make(chan struct{}, 8)
	e.s.RunningCIChanged = func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
		e.s.CancelLocalCI()
	})
	started, err = e.s.startRegisteredCI(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("the freed machine did not start the retry")
	}
	<-ci.started
	close(ci.release)
	released = true
	<-ci.saved
	if saved := ci.results[head]; !saved.Resumed {
		t.Fatalf("the replacement does not carry the marker: %+v", saved)
	}
	// The replacement must leave the run set before the next admission, or a busy
	// key rather than the marker would decide the outcome.
	for runningLocalCIHead(e.s) != "" {
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatal("the replacement never left the run set")
		}
	}

	// The engine stops the replacement too. That second shutdown is the bound, so
	// no third run starts.
	second := stoppedRun()
	second.Repo, second.SHA, second.Resumed = repo, head, true
	ci.results[head] = second
	if act := savedResultActionFor(second); act != settleSavedResult {
		t.Fatalf("a stopped replacement action = %v, want settleSavedResult", act)
	}
	started, err = e.s.startRegisteredCI(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("a second shutdown started another run")
	}
}

// The local-change reader of a trial follows the same rules as the gate: a fresh
// stop waits for the run that replaces it, and a stopped replacement fails the
// trial with the cause instead of waiting for a run that will never come.
func TestStoppedReplacementFailsTheLocalTrial(t *testing.T) {
	definition := watchDynaDefinition(t)
	stopped := stoppedRun()
	stopped.Suites = stopped.Checks
	suites := definition.WorkflowSuites()
	verdict, _, reason, err := localCIStatus(stopped, stopped.Repo, stopped.SHA, definition, suites)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != VerdictPending {
		t.Fatalf("a fresh stop = %s (%s), want pending until it is retried", verdict, reason)
	}
	stopped.Resumed = true
	verdict, _, reason, err = localCIStatus(stopped, stopped.Repo, stopped.SHA, definition, suites)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != VerdictFailed || !strings.Contains(reason, "local CI could not run its checks on pve") {
		t.Fatalf("a stopped replacement = %s (%s), want a failure that names the cause", verdict, reason)
	}
}
