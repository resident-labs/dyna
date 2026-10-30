package watch

import "testing"

// A run with conclusion action_required waits for a maintainer to approve a
// fork pull request. It is not a failure: CI stays pending, no failed run is
// named, and the check reads as waiting rather than failed.
func TestActionRequiredRunWaitsForApproval(t *testing.T) {
	waiting := Run{ID: 40981096357, WorkflowID: 1, Name: "CI", Attempt: 1, Status: "completed", Conclusion: "action_required"}
	snap := Snapshot{Runs: []Run{waiting}}
	state, _, jobs, failedRuns := snap.CIStatus()
	if state != "pending" || len(jobs) != 0 || len(failedRuns) != 0 {
		t.Fatalf("action_required run: state %q jobs %v failed runs %v, want pending with no failures", state, jobs, failedRuns)
	}
	if got := checkState("completed", "action_required"); got == "failure" {
		t.Fatalf("checkState for action_required = %q, want a state that is not failure", got)
	}
}
