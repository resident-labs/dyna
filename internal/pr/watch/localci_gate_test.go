package watch

import (
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

// A pull request head has no local run since local CI serves merge trials only,
// so the base definition's check names can never arrive for it. The hosted
// provider decides that head; only a trial head must cover the names.
func TestPullRequestHeadDoesNotWaitForLocalCheckNames(t *testing.T) {
	definition := watchDynaDefinition(t)
	head := strings.Repeat("c", 40)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "feature", Watching: true, State: "open"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	snap := Snapshot{Checks: []Check{{ID: 1, Name: "go-test", Status: "completed", Conclusion: "success"}}}
	snap.Pull.Head.SHA = head
	snap.Pull.Head.Repo.FullName = r.Repo
	policy := ChangePolicy{RequiredChecks: []string{"go-test"}}
	s := &Service{}
	// A trial head still needs every name its base definition requires.
	if obs := s.registeredLocalObservation(r, snap, policy, plan, localci.Result{}, true, false); obs.Status != VerdictPending {
		t.Fatalf("trial head = %s, want pending", obs.Status)
	}
	obs := s.registeredLocalObservation(r, snap, policy, plan, localci.Result{}, false, false)
	if obs.Status != VerdictPassed {
		t.Fatalf("pull request head = %s (%s), want passed from the hosted provider", obs.Status, obs.Reason)
	}
}

// A manual local run on a pull request head can miss the checks the plan names:
// a docs-only change matches no suite, so the plan wants the changes check. No
// later run completes it, so the head must report absent, not wait for ever.
func TestPartialManualRunOnPullRequestHeadIsAbsent(t *testing.T) {
	definition := watchDynaDefinition(t)
	head := strings.Repeat("d", 40)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "feature", Watching: true, State: "open", HeadSeen: time.Unix(1, 0)}
	plan := localCIPlan{execution: definition, required: definition}
	result := localci.Result{Node: "node", Status: localci.OutcomePassed, Checks: []localci.SuiteResult{{CheckName: "other", Status: localci.OutcomePassed}}}
	var snap Snapshot
	snap.Pull.Head.SHA = head
	s := &Service{}
	if obs := s.registeredLocalObservation(r, snap, ChangePolicy{}, plan, result, true, false); obs.Status != VerdictPending {
		t.Fatalf("trial head = %s, want pending", obs.Status)
	}
	if obs := s.registeredLocalObservation(r, snap, ChangePolicy{}, plan, result, false, false); obs.Status != VerdictAbsent {
		t.Fatalf("pull request head = %s, want absent", obs.Status)
	}
}

// A run that Dyna's own stop cancelled is saved evidence, but it never decides a
// head: the retry decision admits the same commit again, once. A cancellation
// that a superseded head or the owner asked for stays conclusive.
func TestEngineStopResultIsRerunNotDecided(t *testing.T) {
	s := &Service{}
	job := registeredCIJob{record: Record{Key: "pr"}, head: strings.Repeat("a", 40)}
	stopped := localci.Result{SHA: job.head, Node: "node", Status: localci.OutcomeError, Cause: localci.CauseEngineStop}
	if !s.registeredJobResultComplete(t.Context(), job, stopped) {
		t.Fatal("a stopped run is saved evidence, so admission must consult the retry decision")
	}
	if act := savedResultActionFor(stopped); act != retryStoppedRun {
		t.Fatalf("a stopped run action = %v, want retryStoppedRun", act)
	}
	stopped.Resumed = true
	if act := savedResultActionFor(stopped); act != settleSavedResult {
		t.Fatalf("a stopped replacement action = %v, want settleSavedResult", act)
	}
	stopped.Resumed = false
	cancelled := localci.Result{SHA: job.head, Node: "node", Status: localci.OutcomeError, Cause: localci.CauseCancelled}
	if !s.registeredJobResultComplete(t.Context(), job, cancelled) {
		t.Fatal("a plain cancellation stopped being conclusive")
	}
	failed := localci.Result{SHA: job.head, Node: "node", Status: localci.OutcomeFailed}
	if !s.registeredJobResultComplete(t.Context(), job, failed) {
		t.Fatal("a failed run stopped being conclusive")
	}
	// The trial's own verdict reads the same evidence: an engine stop leaves it
	// pending, so the batch keeps running and the trial is repeated.
	if verdict, _, _ := registeredResultStatus(stopped); verdict != VerdictPending {
		t.Fatalf("an engine-stop result decided %s, want pending", verdict)
	}
}

// A manual local run on a pull request head can die with its machine. Nothing
// retries it, so its error result must not hold the head in "waiting for CI"
// and keep the PR out of every later merge trial.
func TestLostManualRunOnPullRequestHeadIsAbsent(t *testing.T) {
	head := strings.Repeat("e", 40)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "feature", Watching: true, State: "open", HeadSeen: time.Unix(1, 0)}
	lost := localci.Result{SHA: head, Node: "pve", Status: localci.OutcomeError, Cause: localci.CauseSSHDrop}
	var snap Snapshot
	snap.Pull.Head.SHA = head
	s := &Service{}
	if obs := s.registeredLocalObservation(r, snap, ChangePolicy{}, localCIPlan{}, lost, false, false); obs.Status != VerdictAbsent {
		t.Fatalf("pull request head = %s (%s), want absent", obs.Status, obs.Reason)
	}
}

func localPassGateFixture(t *testing.T) (Record, localCIPlan, localci.Result) {
	t.Helper()
	definition, err := localci.ParseDefinition([]byte(`version: 1
trial: [test]
checks:
  - id: unit
    image: python:3.13-slim
    command: python -m unittest
suites:
  - id: test
    checks: [unit]
`))
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Head: "head"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	return record, plan, passedResult([]localci.CheckName{"unit"})
}

// A cancelled hosted job is not a selection signal, so it holds a local pass
// back only while a base-required name keeps asking for it. The engine's own
// cancellation removed the hosted row, so the wait reason reports that no
// hosted result exists for the required name.
func TestCancelledHostedOnlyCheckBlocksLocalPass(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	policy := ChangePolicy{RequiredChecks: []string{"pi"}}
	snap := Snapshot{
		Checks: []Check{actionsCheck(Check{ID: 1, Name: "pi", Status: "completed", Conclusion: "cancelled"})},
		Runs:   []Run{{ID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "cancelled"}},
	}
	snap.Checks[0].CheckSuite.ID = 1
	snap = withoutLocalHostedCancellations(snap, localci.HostedRuns{Attempts: map[int64]int{1: 1}}, plan.execution)
	snap.Pull.Head.SHA = record.Head
	service := &Service{}
	observation := service.registeredLocalObservation(record, snap, policy, plan, result, false, true)
	if observation.Status != VerdictPending || !slices.Contains(observation.Jobs, "pi") {
		t.Fatalf("a base-required name stopped holding the local pass: %+v", observation)
	}
	wantReason := "pi: no hosted result, no local check"
	if observation.Reason != wantReason {
		t.Fatalf("reason = %q, want %q", observation.Reason, wantReason)
	}
	snap.localCI = observation
	stored := record
	service.applyCI(&State{}, &stored, snap, string(observation.Status), observation.Key, observation.Jobs, nil, false, newPollNotices(&stored))
	if stored.CIReason != wantReason {
		t.Fatalf("stored PR reason = %q, want %q", stored.CIReason, wantReason)
	}
}

func TestPendingNeededChecksBlockLocalPass(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	snap := Snapshot{Checks: []Check{
		actionsCheck(Check{ID: 1, Name: "pi", Status: "in_progress"}),
		actionsCheck(Check{ID: 2, Name: "computer-smoke", Status: "queued"}),
		actionsCheck(Check{ID: 3, Name: "go-test-shard (9)", Status: "in_progress"}),
	}}
	snap.Pull.Head.SHA = record.Head
	observation := (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status != VerdictPending {
		t.Fatalf("pending needed checks did not block the local pass: %+v", observation)
	}
	want := []string{"computer-smoke", "go-test-shard (9)", "pi"}
	if !slices.Equal(observation.Jobs, want) {
		t.Fatalf("missing checks = %v, want %v", observation.Jobs, want)
	}
	for _, name := range want {
		if !strings.Contains(observation.Reason, name+": hosted check pending, no local check") {
			t.Fatalf("reason %q does not explain %s", observation.Reason, name)
		}
	}
}

func TestLocalPassSatisfiesCancelledHostedDuplicate(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	result.Checks = append(result.Checks, localci.SuiteResult{CheckName: "pi", Status: localci.OutcomePassed})
	snap := Snapshot{
		Checks: []Check{actionsCheck(Check{ID: 1, Name: "pi", Status: "completed", Conclusion: "cancelled"})},
		Runs:   []Run{{ID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "cancelled"}},
	}
	snap.Checks[0].CheckSuite.ID = 1
	snap = withoutLocalHostedCancellations(snap, localci.HostedRuns{Attempts: map[int64]int{1: 1}}, plan.execution)
	snap.Pull.Head.SHA = record.Head
	observation := (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status != VerdictPassed {
		t.Fatalf("a cancelled duplicate blocked the matching local pass: %+v", observation)
	}
}

// In local mode the engine cancels hosted runs on purpose. A cancelled hosted
// job never tested the head, so it is not a selection signal: a hosted-only
// helper such as "changes" must not become a need that no path can satisfy.
func TestCancelledHostedOnlyJobDoesNotBlockLocalPass(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	snap := Snapshot{
		Checks: []Check{actionsCheck(Check{ID: 1, Name: "changes", Status: "completed", Conclusion: "cancelled"})},
		Runs:   []Run{{ID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "cancelled"}},
	}
	snap.Checks[0].CheckSuite.ID = 1
	snap = withoutLocalHostedCancellations(snap, localci.HostedRuns{Attempts: map[int64]int{1: 1}}, plan.execution)
	snap.Pull.Head.SHA = record.Head
	observation := (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status != VerdictPassed {
		t.Fatalf("a cancelled hosted-only job blocked the local pass: %+v", observation)
	}
}

// A matrix job that never expanded keeps GitHub's template name, and the
// engine's cancellation ends it before any shard starts. Local CI reports the
// real shard names, so the template row must not become a need that never
// resolves.
func TestCancelledMatrixJobTemplateWithLocalShardsIsReady(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	result.Checks = append(result.Checks,
		localci.SuiteResult{CheckName: "go-test-shard (0)", Status: localci.OutcomePassed},
		localci.SuiteResult{CheckName: "go-test-shard (1)", Status: localci.OutcomePassed},
	)
	snap := Snapshot{
		Checks: []Check{actionsCheck(Check{ID: 1, Name: "go-test-shard (${{ matrix.shard }})", Status: "completed", Conclusion: "cancelled"})},
		Runs:   []Run{{ID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "cancelled"}},
	}
	snap.Checks[0].CheckSuite.ID = 1
	snap = withoutLocalHostedCancellations(snap, localci.HostedRuns{Attempts: map[int64]int{1: 1}}, plan.execution)
	snap.Pull.Head.SHA = record.Head
	observation := (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status != VerdictPassed {
		t.Fatalf("a cancelled matrix template job blocked passing local shards: %+v", observation)
	}
}

// A hosted job that reached failure is real evidence and still blocks the
// local pass: dropping cancelled jobs from selection must never hide a failed
// one.
func TestFailedHostedJobStillBlocksLocalPass(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	snap := Snapshot{Checks: []Check{actionsCheck(Check{ID: 1, Name: "changes", Status: "completed", Conclusion: "failure"})}}
	snap.Checks[0].CheckSuite.ID = 1
	snap.Pull.Head.SHA = record.Head
	observation := (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status == VerdictPassed || !slices.Contains(observation.Jobs, "changes") {
		t.Fatalf("a failed hosted job stopped blocking the local pass: %+v", observation)
	}
}

func TestSameNamedChecksInDifferentWorkflowsNeedSeparatePasses(t *testing.T) {
	record, plan, result := localPassGateFixture(t)
	pending := actionsCheck(Check{ID: 1, Name: "build", Status: "in_progress"})
	pending.CheckSuite.ID = 1
	passed := actionsCheck(Check{ID: 2, Name: "build", Status: "completed", Conclusion: "success"})
	passed.CheckSuite.ID = 2
	snap := Snapshot{
		Checks: []Check{pending, passed},
		Runs: []Run{
			{ID: 1, WorkflowID: 1, CheckSuiteID: 1, Status: "in_progress"},
			{ID: 2, WorkflowID: 2, CheckSuiteID: 2, Status: "completed", Conclusion: "success"},
		},
	}
	snap.Pull.Head.SHA = record.Head
	observation := (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status != VerdictPending || !slices.Contains(observation.Jobs, "build") {
		t.Fatalf("one workflow's pass replaced another workflow's pending check: %+v", observation)
	}
	result.Checks = append(result.Checks, localci.SuiteResult{CheckName: "build", Status: localci.OutcomePassed})
	observation = (&Service{}).registeredLocalObservation(record, snap, ChangePolicy{}, plan, result, false, true)
	if observation.Status != VerdictPassed {
		t.Fatalf("a matching local pass did not satisfy both selected identities: %+v", observation)
	}
}
