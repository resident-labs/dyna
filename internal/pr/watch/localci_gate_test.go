package watch

import (
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
