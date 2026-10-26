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
func TestTrialWithoutAHostedFallbackFailsWithItsCause(t *testing.T) {
	definition := watchDynaDefinition(t)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: strings.Repeat("a", 40), Branch: "dyna/merge-trial/1", Watching: true, State: "open"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	s := &Service{}

	// Dyna stopped the run and no replacement has started yet: the retry is
	// pending, so the trial waits with a truthful reason.
	stopped := stoppedRun()
	obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, true, false)
	if obs.Status != VerdictPending {
		t.Fatalf("stopped run = %s, want pending until the retry starts", obs.Status)
	}
	if strings.Contains(obs.Reason, "hosted") {
		t.Fatalf("retry reason names a hosted fallback that does not exist: %q", obs.Reason)
	}

	// The replacement was stopped too, or no retry is possible, and no hosted run
	// exists to fall back to: the trial fails and names the cause.
	stopped.Resumed = true
	obs = s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, true, false)
	if obs.Status != VerdictFailed {
		t.Fatalf("settled interruption = %s, want failed", obs.Status)
	}
	if !strings.Contains(obs.Reason, "local CI could not run its checks on pve") || !strings.Contains(obs.Reason, "engine") {
		t.Fatalf("failure reason does not name the node and cause: %q", obs.Reason)
	}
	if len(obs.Jobs) == 0 {
		t.Fatal("failure names no check that did not run")
	}

	// A hosted run that exists or can be restarted still decides, so the same
	// result waits for it instead of failing the trial.
	if obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, true, true); obs.Status != VerdictPending {
		t.Fatalf("hosted fallback available = %s, want it to wait for that run", obs.Status)
	}
	// A machine failure with its retry already spent behaves the same way.
	dropped := stoppedRun()
	dropped.Cause = localci.CauseSSHDrop
	dropped.RetriedFrom = dropped.Node
	for i := range dropped.Checks {
		dropped.Checks[i].Cause = localci.CauseSSHDrop
	}
	if obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, dropped, true, false); obs.Status != VerdictFailed {
		t.Fatalf("spent ssh-drop retry = %s, want failed with the cause", obs.Status)
	}
	// A pull request head is never failed by a local run it does not have.
	if obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, stopped, false, false); obs.Status == VerdictFailed {
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

	// The engine stops the replacement too. That second shutdown is the bound: the
	// saved result decides, and nothing starts a third run.
	second := stoppedRun()
	second.Repo, second.SHA, second.Resumed = repo, head, true
	ci.results[head] = second
	if act := savedResultActionFor(second); act != settleSavedResult {
		t.Fatalf("a stopped replacement action = %v, want settleSavedResult", act)
	}
}

// The reader a trial uses must admit the retry a stopped run deserves, and fail a
// spent retry with the cause. A reader that answered a fresh stop instead would
// keep a sync head from ever retrying, because it has no other scheduler entry.
func TestSavedTrialCIAdmitsARetryableStopAndFailsASpentOne(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cause   localci.FailureCause
		resumed bool
		want    CIVerdict
	}{
		{name: "engine stop retries", cause: localci.CauseEngineStop, want: VerdictPending},
		{name: "cancelled retries", cause: localci.CauseCancelled, want: VerdictPending},
		{name: "spent stop fails", cause: localci.CauseEngineStop, resumed: true, want: VerdictFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, record, dir, ci := localCIEnv(t)
			repo, err := localci.CanonicalRepo(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			head := record.Head
			stopped := stoppedRun()
			stopped.Repo, stopped.SHA, stopped.Cause, stopped.Resumed = repo, head, tc.cause, tc.resumed
			for i := range stopped.Checks {
				stopped.Checks[i].Cause = tc.cause
			}
			ci.results[head] = stopped
			ci.started = make(chan struct{}, 1)
			ci.release = make(chan struct{})
			released := false
			t.Cleanup(func() {
				if !released {
					close(ci.release)
				}
				e.s.CancelLocalCI()
			})
			trial, err := e.s.localTrialCI(t.Context(), record, head)
			if err != nil {
				t.Fatal(err)
			}
			if trial.Status != tc.want {
				t.Fatalf("reader = %s (%s), want %s", trial.Status, trial.Reason, tc.want)
			}
			if tc.want == VerdictFailed {
				if head := runningLocalCIHead(e.s); head != "" {
					t.Fatalf("a spent retry started a run for %s", head)
				}
				if !strings.Contains(trial.Reason, "local CI could not run its checks") {
					t.Fatalf("failure reason = %q, want the cause", trial.Reason)
				}
				return
			}
			// The reader admitted the retry, so it is in the run set at once.
			if head := runningLocalCIHead(e.s); head == "" {
				t.Fatal("the reader did not admit the retry")
			}
			<-ci.started
			close(ci.release)
			released = true
		})
	}
}

// A machine failure whose retry has nowhere else to go runs on the machine that
// failed, once. Without it, a room whose only eligible machine is the failed one
// waits with nothing running, which is what D-151 reported.
func TestMachineFailureRerunsOnTheSameMachineWhenNoOtherCanTakeIt(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	repo, err := localci.CanonicalRepo(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	head := record.Head
	dropped := stoppedRun()
	dropped.Repo, dropped.SHA = repo, head
	dropped.Node = "hermeslab"
	dropped.Cause = localci.CauseSSHDrop
	for i := range dropped.Checks {
		dropped.Checks[i].Cause = localci.CauseSSHDrop
	}
	ci.results[head] = dropped
	// One machine only, and it is the one that failed.
	ci.nodes = []localci.Node{{Name: "hermeslab", Slots: 4, Enabled: true}}
	job := registeredCIJob{record: record, head: head, branch: record.Branch, trial: true, wholeSuite: true}

	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	ci.saved = make(chan struct{}, 1)
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
		e.s.CancelLocalCI()
	})
	started, err := e.s.startRegisteredCI(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("the failed machine was the only one that could run the retry, and nothing started")
	}
	<-ci.started
	close(ci.release)
	released = true
	<-ci.saved
	saved := ci.results[head]
	if saved.Node != "hermeslab" || !saved.Resumed {
		t.Fatalf("the same-machine replacement = %+v, want hermeslab marked resumed", saved)
	}
}

// An engine stop is replaced once. If that replacement then loses its SSH
// connection, its retry is spent: with one eligible machine and no hosted
// fallback the trial must fail with the cause, not wait for a retry that
// admission will never start.
func TestResumedReplacementThatLosesSSHFailsTheTrial(t *testing.T) {
	resumed := stoppedRun()
	resumed.Cause = localci.CauseSSHDrop
	for i := range resumed.Checks {
		resumed.Checks[i].Cause = localci.CauseSSHDrop
	}
	resumed.Resumed = true
	if act := savedResultActionFor(resumed); act != settleSavedResult {
		t.Fatalf("a resumed machine failure action = %v, want settleSavedResult", act)
	}

	definition := watchDynaDefinition(t)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: strings.Repeat("a", 40), Branch: "dyna/merge-trial/1", Watching: true, State: "open"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	obs := (&Service{}).registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, resumed, true, false)
	if obs.Status != VerdictFailed {
		t.Fatalf("resumed machine failure = %s (%s), want failed with the cause", obs.Status, obs.Reason)
	}

	// Admission agrees: one eligible machine, which is the one that failed, and
	// nothing starts because the retry is spent.
	e, record, dir, ci := localCIEnv(t)
	repo, err := localci.CanonicalRepo(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	head := record.Head
	saved := stoppedRun()
	saved.Repo, saved.SHA, saved.Node, saved.Cause, saved.Resumed = repo, head, "hermeslab", localci.CauseSSHDrop, true
	for i := range saved.Checks {
		saved.Checks[i].Cause = localci.CauseSSHDrop
	}
	ci.results[head] = saved
	ci.nodes = []localci.Node{{Name: "hermeslab", Slots: 4, Enabled: true}}
	ci.started = make(chan struct{}, 1)
	started, err := e.s.startRegisteredCI(t.Context(), registeredCIJob{record: record, head: head, branch: record.Branch, trial: true, wholeSuite: true})
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("a spent retry started another run")
	}
}
