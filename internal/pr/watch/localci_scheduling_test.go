package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

// preparationFailure is the result shape D-156 reported: the run never started,
// so it has no checks, and the error is the fetch that failed.
func preparationFailure() localci.Result {
	return localci.Result{
		Repo: "repo", SHA: strings.Repeat("a", 40), Node: "pve", Status: localci.OutcomeError,
		Cause: localci.CauseScheduling,
		Error: "fetch local CI head and base: exit status 128: fatal: couldn't find remote ref",
	}
}

// A trial whose run never started is not a failed test. It waits for the one
// retry, and a spent retry fails with the scheduling cause and the real error, so
// nobody reads it as a broken commit.
func TestTrialWhoseRunNeverStartedFailsWithTheSchedulingCause(t *testing.T) {
	definition := watchDynaDefinition(t)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: strings.Repeat("a", 40), Branch: "dyna/merge-trial/1", Watching: true, State: "open"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	s := &Service{}

	never := preparationFailure()
	if act := savedResultActionFor(never); act != retryStoppedRun {
		t.Fatalf("a run that never started action = %v, want one retry", act)
	}
	// The first failure waits: the retry is what decides.
	if obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, never, true, false); obs.Status != VerdictPending {
		t.Fatalf("first scheduling failure = %s, want pending for the retry", obs.Status)
	}
	// A spent retry fails with the cause, not as a test failure.
	never.Resumed = true
	obs := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, never, true, false)
	if obs.Status != VerdictFailed {
		t.Fatalf("spent scheduling failure = %s, want failed", obs.Status)
	}
	if !strings.Contains(obs.Reason, "could not start") || !strings.Contains(obs.Reason, "remote ref") {
		t.Fatalf("failure reason does not name the scheduling cause and the error: %q", obs.Reason)
	}
	// The local lane's reader says the same.
	never.Suites = nil
	verdict, logs, reason, err := localCIStatus(never, never.Repo, never.SHA, definition, plan.suites)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != VerdictFailed || !strings.Contains(reason, "could not start") {
		t.Fatalf("local reader = %s (%s), want a scheduling failure", verdict, reason)
	}
	if !strings.Contains(logs, "remote ref") {
		t.Fatalf("local reader drops the real error: %q", logs)
	}
}

// The D-156 follow-up: a trial with no saved result at all has not been admitted
// yet, so it must wait. Only a finished infrastructure failure may fail it, never
// the absence of a result, which is what the rule must never mistake for proof.
func TestTrialWithNoSavedResultWaitsForAdmission(t *testing.T) {
	definition := watchDynaDefinition(t)
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: strings.Repeat("a", 40), Branch: "dyna/merge-trial/1", Watching: true, State: "open"}
	plan := localCIPlan{execution: definition, required: definition, suites: definition.WorkflowSuites()}
	obs := (&Service{}).registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, plan, localci.Result{}, true, false)
	if obs.Status != VerdictPending {
		t.Fatalf("no result yet = %s (%s), want pending until a run is admitted", obs.Status, obs.Reason)
	}
	// A run in flight is named, so a reader can see admission happened.
	if obs.Status == VerdictFailed {
		t.Fatalf("an admitted run was failed before it ended: %s", obs.Reason)
	}
}

// A trial branch is pushed just before the trial fetches its head, so the first
// fetch can arrive before GitHub publishes the ref. That is a wait, not a
// scheduling failure: the fetch is tried again, bounded, and only a fetch that
// still cannot see the ref fails the trial.
func TestFetchWaitsForAJustPushedTrialRef(t *testing.T) {
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "head\n")
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: head, Base: base, Branch: "dyna/merge-trial/1", RepoPath: repo}
	job := registeredCIJob{record: r, head: head, base: base, branch: r.Branch, trial: true, wholeSuite: true}
	s := &Service{}
	var waits []time.Duration
	s.Wait = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	calls := 0
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		calls++
		if calls == 1 {
			fmt.Fprintf(cmd.Stderr, "fatal: couldn't find remote ref %s\n", head)
			return fmt.Errorf("exit status 128")
		}
		return nil
	}
	if _, err := s.prepareRegisteredCI(context.Background(), job); err != nil {
		t.Fatalf("a just-pushed ref was not waited for: %v", err)
	}
	if calls != 2 || len(waits) != 1 {
		t.Fatalf("calls=%d waits=%v, want one more fetch after one wait", calls, waits)
	}
	// A failure that is not a missing ref fails at once, with git's own words.
	calls = 0
	waits = nil
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		calls++
		fmt.Fprintln(cmd.Stderr, "fatal: Authentication failed for repository")
		return fmt.Errorf("exit status 128")
	}
	if _, err := s.prepareRegisteredCI(context.Background(), job); err == nil || !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatalf("auth failure = %v, want the git error with its stderr", err)
	}
	if calls != 1 || len(waits) != 0 {
		t.Fatalf("auth failure retried: calls=%d waits=%v", calls, waits)
	}
	// A ref that never appears is bounded, not retried for ever.
	calls = 0
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		calls++
		fmt.Fprintf(cmd.Stderr, "fatal: couldn't find remote ref %s\n", head)
		return fmt.Errorf("exit status 128")
	}
	if _, err := s.prepareRegisteredCI(context.Background(), job); err == nil || !strings.Contains(err.Error(), "couldn't find remote ref") {
		t.Fatalf("missing ref = %v, want the bounded failure", err)
	}
	if calls != ciRefFetchAttempts {
		t.Fatalf("fetch attempts = %d, want %d", calls, ciRefFetchAttempts)
	}
}
