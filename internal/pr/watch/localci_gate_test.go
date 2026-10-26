package watch

import (
	"strings"
	"testing"

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
	if obs := s.registeredLocalObservation(r, snap, policy, plan, localci.Result{}, true); obs.Status != VerdictPending {
		t.Fatalf("trial head = %s, want pending", obs.Status)
	}
	obs := s.registeredLocalObservation(r, snap, policy, plan, localci.Result{}, false)
	if obs.Status != VerdictPassed {
		t.Fatalf("pull request head = %s (%s), want passed from the hosted provider", obs.Status, obs.Reason)
	}
}

// A run that Dyna's own stop cancelled is not a finished head: the scheduler
// admits the same commit again. A cancellation that a superseded head or the
// owner asked for stays conclusive.
func TestEngineStopResultIsRerunNotComplete(t *testing.T) {
	s := &Service{}
	job := registeredCIJob{record: Record{Key: "pr"}, head: strings.Repeat("a", 40)}
	stopped := localci.Result{SHA: job.head, Node: "node", Status: localci.OutcomeError, Cause: localci.CauseEngineStop}
	if s.registeredJobResultComplete(t.Context(), job, stopped) {
		t.Fatal("an engine-stop result counted as a finished head")
	}
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
