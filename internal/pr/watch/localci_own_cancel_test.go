package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// ownCancelRegistry is a local CI registry whose hosted cancellations are
// recorded, as the engine records the runs it cancels for a local replacement.
type ownCancelRegistry struct {
	fakeLocalCI
	hosted localci.HostedRuns
}

func (o *ownCancelRegistry) LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error) {
	return o.hosted, nil
}
func (o *ownCancelRegistry) SaveHostedRuns(_ context.Context, _, _ string, runs localci.HostedRuns) error {
	o.hosted = runs
	return nil
}

// suiteCheck is a check-run of one suite.
func suiteCheck(id int64, name, conclusion string, suite int64) Check {
	check := actionsCheck(Check{ID: id, Name: name, Status: "completed", Conclusion: conclusion})
	check.CheckSuite.ID = suite
	return check
}

// D-168: a trial's local run cancels the trial branch's queued hosted push run.
// The workflow's aggregate jobs then report that cancellation back as failures
// ("A Go job ended with cancelled"), and the repository definition need not name
// those aggregates at all. The saved record says the cancellation is Dyna's own,
// so the run never tested the head: the trial must wait for its local run, not
// fail and pause the queue.
func TestOwnCancelledHostedRunDoesNotFailTheTrial(t *testing.T) {
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "trial\n")
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: head, Branch: "dyna/merge-trial/331", RepoPath: repo, Base: base, Watching: true, State: "open"}
	s := &Service{Store: &Store{Root: t.TempDir() + "/prs"}}
	if err := s.Store.update(func(st *State) error {
		st.MergeTrials = map[string]*state.MergeTrial{"owner/repo": {Branch: r.Branch, Head: head, Base: base}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const suite = int64(42)
	run := Run{ID: 7, Attempt: 1, CheckSuiteID: suite, HeadSHA: head, HeadBranch: r.Branch,
		Event: "push", Status: "completed", Conclusion: "cancelled", Path: ".github/workflows/ci.yml"}
	checks := []Check{
		suiteCheck(1, "go-test", "failure", suite),
		suiteCheck(2, "frontend", "failure", suite),
		suiteCheck(3, "go-static", "cancelled", suite),
		suiteCheck(4, "go-test-shard (${{ matrix.shard }})", "cancelled", suite),
		suiteCheck(5, "frontend build", "cancelled", suite),
	}
	registry := &ownCancelRegistry{fakeLocalCI: fakeLocalCI{results: map[string]localci.Result{}}}
	registry.hosted = localci.HostedRuns{IDs: []int64{7}, Attempts: map[int64]int{7: 1}}
	s.LocalCIRegistry = registry
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		switch {
		case strings.Contains(args, "actions/runs?"):
			return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: []Run{run}})
		case strings.Contains(args, "check-runs?"):
			return writeFakeJSON(cmd.Stdout, checkRunsResponse{CheckRuns: checks})
		case strings.Contains(args, "/statuses?"):
			return writeFakeJSON(cmd.Stdout, []Status{})
		}
		return fmt.Errorf("unexpected call %s", args)
	}
	ci, err := s.mergeTrialCI(context.Background(), r, r.Branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Status != VerdictPending {
		t.Fatalf("trial = %s (%s), want pending while its local run decides", ci.Status, ci.Reason)
	}
	if strings.Contains(ci.Reason, "cancelled") {
		t.Fatalf("the reason still blames a cancellation: %q", ci.Reason)
	}
}

// The start race cuts both ways: a job Dyna's queued-status read missed can
// start and genuinely fail before the cancel lands, and a standalone job the
// definition gives no group (go-static) is a real runner, not an aggregate.
// Its failure must stay evidence even inside Dyna's own cancelled run, so the
// row echoes around it cannot mask it and a local pass cannot override it.
func TestOwnCancelledRunWithAStandaloneFailureStillFailsTheTrial(t *testing.T) {
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "trial\n")
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: head, Branch: "dyna/merge-trial/333", RepoPath: repo, Base: base, Watching: true, State: "open"}
	s := &Service{Store: &Store{Root: t.TempDir() + "/prs"}}
	if err := s.Store.update(func(st *State) error {
		st.MergeTrials = map[string]*state.MergeTrial{"owner/repo": {Branch: r.Branch, Head: head, Base: base}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const suite = int64(44)
	run := Run{ID: 9, Attempt: 1, CheckSuiteID: suite, HeadSHA: head, HeadBranch: r.Branch,
		Event: "push", Status: "completed", Conclusion: "cancelled", Path: ".github/workflows/ci.yml"}
	checks := []Check{
		suiteCheck(1, "go-static", "failure", suite),
		suiteCheck(2, "go-test", "failure", suite),
		suiteCheck(3, "frontend", "failure", suite),
		suiteCheck(4, "go-test-shard (${{ matrix.shard }})", "cancelled", suite),
	}
	registry := &ownCancelRegistry{fakeLocalCI: fakeLocalCI{results: map[string]localci.Result{}}}
	registry.hosted = localci.HostedRuns{IDs: []int64{9}, Attempts: map[int64]int{9: 1}}
	s.LocalCIRegistry = registry
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		switch {
		case strings.Contains(args, "actions/runs?"):
			return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: []Run{run}})
		case strings.Contains(args, "check-runs?"):
			return writeFakeJSON(cmd.Stdout, checkRunsResponse{CheckRuns: checks})
		case strings.Contains(args, "/statuses?"):
			return writeFakeJSON(cmd.Stdout, []Status{})
		}
		return fmt.Errorf("unexpected call %s", args)
	}
	ci, err := s.mergeTrialCI(context.Background(), r, r.Branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Status != VerdictFailed {
		t.Fatalf("trial = %s (%s), want failed: a standalone job that really ran and failed is evidence even under Dyna's own cancellation", ci.Status, ci.Reason)
	}
	if !strings.Contains(ci.Reason, "go-static") {
		t.Fatalf("the reason lost the standalone failure: %q", ci.Reason)
	}
}

// The same shape without a saved cancellation is someone else's run: a failed
// check in it is real evidence, and the trial still fails. Only Dyna's own
// cancellations are dropped.
func TestForeignCancelledRunWithAFailedCheckStillFailsTheTrial(t *testing.T) {
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "trial\n")
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo",
		Head: head, Branch: "dyna/merge-trial/332", RepoPath: repo, Base: base, Watching: true, State: "open"}
	s := &Service{Store: &Store{Root: t.TempDir() + "/prs"}}
	if err := s.Store.update(func(st *State) error {
		st.MergeTrials = map[string]*state.MergeTrial{"owner/repo": {Branch: r.Branch, Head: head, Base: base}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	const suite = int64(43)
	run := Run{ID: 8, Attempt: 1, CheckSuiteID: suite, HeadSHA: head, HeadBranch: r.Branch,
		Event: "push", Status: "completed", Conclusion: "cancelled", Path: ".github/workflows/ci.yml"}
	checks := []Check{
		suiteCheck(1, "go-test", "failure", suite),
		suiteCheck(2, "go-static", "cancelled", suite),
	}
	registry := &ownCancelRegistry{fakeLocalCI: fakeLocalCI{results: map[string]localci.Result{}}}
	s.LocalCIRegistry = registry
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		switch {
		case strings.Contains(args, "actions/runs?"):
			return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: []Run{run}})
		case strings.Contains(args, "check-runs?"):
			return writeFakeJSON(cmd.Stdout, checkRunsResponse{CheckRuns: checks})
		case strings.Contains(args, "/statuses?"):
			return writeFakeJSON(cmd.Stdout, []Status{})
		}
		return fmt.Errorf("unexpected call %s", args)
	}
	ci, err := s.mergeTrialCI(context.Background(), r, r.Branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Status != VerdictFailed {
		t.Fatalf("trial = %s (%s), want failed: a foreign run's failed check is real evidence", ci.Status, ci.Reason)
	}
}

// A provider name does not declare that the hosted job only repeats its
// requirements: here the hosted "unit" job runs tests directly. Under Dyna's own
// cancellation its failure stays evidence, while a check the definition marks
// hostedAggregate is still an echo.
func TestOwnCancellationKeepsAProviderNamedJobUnlessDeclaredAggregate(t *testing.T) {
	definition, err := localci.ParseDefinition([]byte(`version: 1
trial: [unit]
checks:
  - id: local-unit
    name: local-unit
    providerGroup: unit
    image: node:22
    command: bash run.sh
  - id: gate
    name: gate
    hostedAggregate: true
    image: node:22
    command: bash gate.sh
suites:
  - id: unit
    checks: [local-unit, gate]
`))
	if err != nil {
		t.Fatal(err)
	}
	const suite = int64(45)
	for _, tc := range []struct {
		name, failed string
		want         bool
	}{
		{"provider-named job that ran tests", "unit", true},
		{"declared aggregate job", "gate", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := []Check{suiteCheck(1, tc.failed, "failure", suite), suiteCheck(2, "other", "cancelled", suite)}
			class := classifyHostedSuite(definition, checks, suite, true, true)
			if class.realFailure != tc.want {
				t.Fatalf("real failure = %t, want %t", class.realFailure, tc.want)
			}
		})
	}
}
