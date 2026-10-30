package watch

import (
	"testing"

	"dyna-agent/internal/localci"
	cisched "dyna-agent/internal/pr/watch/ci"
)

// A push or checkout that git rejected is a machine fault. It keeps the git
// error as job evidence, and the run moves to another machine once. A retry
// that already ran stands, so the trial can fail with that evidence.
func TestGitTransportFailureRetriesOnAnotherMachine(t *testing.T) {
	gitFailure := localci.Result{
		Repo: "repo", SHA: "head", Node: "hermeslab", Status: localci.OutcomeError, Cause: localci.CauseGit,
		Error: "push commit to node: exit status 1: connection refused",
		Checks: []localci.SuiteResult{{
			CheckName: "git-push", Status: localci.OutcomeError, Cause: localci.CauseGit,
			Error: "push commit to node: exit status 1: connection refused",
		}},
	}
	if got := cisched.MachineFailureRetryFrom(gitFailure); got != "hermeslab" {
		t.Fatalf("git transport failure retried from %q, want hermeslab", got)
	}
	if act := cisched.SavedResultActionFor(gitFailure); act != cisched.RetryOnAnotherMachine {
		t.Fatalf("action = %v, want a retry on another machine", act)
	}
	status, _, _, err := localCIStatus(gitFailure, "repo", "head", nil, nil)
	if err != nil || status != VerdictPending {
		t.Fatalf("unretried git failure = %s (%v), want pending", status, err)
	}
	spent := gitFailure
	spent.RetriedFrom = "arch"
	if got := cisched.MachineFailureRetryFrom(spent); got != "" {
		t.Fatalf("a spent git retry started again from %q", got)
	}
	status, _, _, err = localCIStatus(spent, "repo", "head", nil, nil)
	if err != nil || status != VerdictFailed {
		t.Fatalf("spent git retry = %s (%v), want failed", status, err)
	}
}
