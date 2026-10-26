package watch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestRegisteredCIGateUsesBaseAndCommandsUseHead(t *testing.T) {
	repo, _ := changeRepo(t)
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: unit
    image: python:3.13-slim
    command: python -m pytest
  - id: lint
    image: python:3.13-slim
    command: python -m ruff check
suites:
  - id: test
    checks: [unit, lint]
`)
	base := changeCommit(t, repo, "base definition\n")
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: unit
    image: python:3.13-slim
    command: python -m pytest --new
suites:
  - id: test
    checks: [unit]
`)
	head := changeCommit(t, repo, "head definition\n")
	record := Record{Kind: "github", Head: head, Base: base, Branch: "dyna/merge-trial/1", RepoPath: repo}
	plan, err := (&Service{}).planRegisteredCI(t.Context(), registeredCIJob{record: record, head: head, base: base, trial: true, wholeSuite: true})
	if err != nil {
		t.Fatal(err)
	}
	checks, ok := plan.execution.ExecutableChecks("test")
	if !ok || len(checks) != 1 || checks[0].Command != "python -m pytest --new" {
		t.Fatalf("execution checks = %+v, want the head command", checks)
	}
	required := plan.required.RequiredChecks(plan.suites)
	if !slices.Contains(required, localci.CheckName("unit")) || !slices.Contains(required, localci.CheckName("lint")) {
		t.Fatalf("base-required checks = %v", required)
	}

	result := localci.Result{Node: "worker", Status: localci.OutcomePassed}
	for _, name := range required {
		if name != "lint" {
			result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: localci.OutcomePassed})
		}
	}
	observation := (&Service{}).registeredLocalObservation(record, Snapshot{}, ChangePolicy{}, plan, result, true, false)
	if observation.Status != VerdictPending || !slices.Contains(observation.Jobs, "lint") {
		t.Fatalf("head removed a base check and passed: %+v", observation)
	}
	result.Checks = append(result.Checks, localci.SuiteResult{CheckName: "lint", Status: localci.OutcomePassed})
	observation = (&Service{}).registeredLocalObservation(record, Snapshot{}, ChangePolicy{}, plan, result, true, false)
	if observation.Status != VerdictPassed {
		t.Fatalf("complete base-defined result did not pass: %+v", observation)
	}
}

func TestRepositoryWithoutBaseDefinitionUsesHostedCI(t *testing.T) {
	repo, _ := changeRepo(t)
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(localci.DefinitionPath))); err != nil {
		t.Fatal(err)
	}
	base := changeCommit(t, repo, "remove local CI\n")
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: pytest
    image: python:3.13-slim
    command: python -m pytest
suites:
  - id: test
    checks: [pytest]
`)
	head := changeCommit(t, repo, "add local CI later\n")
	record := Record{Kind: "github", Head: head, Base: base, Branch: "feature", Repo: "owner/repo", RepoPath: repo}
	snap := Snapshot{Checks: []Check{actionsCheck(Check{Name: "hosted", Status: "completed", Conclusion: "success"})}}
	snap.Pull.Head.SHA, snap.Pull.Head.Repo.FullName = head, record.Repo
	service := &Service{LocalCIRegistry: &requiredCIRegistry{}}
	got, err := service.withRegisteredCI(t.Context(), record, snap, ChangePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if got.localCIConfig != state.LocalCIConfigNotConfigured || got.localCI != nil {
		t.Fatalf("unconfigured repository acquired local CI: %+v", got.localCI)
	}
	if state, _, _, _ := got.CIStatus(); state != "passed" {
		t.Fatalf("hosted CI did not remain authoritative: %s", state)
	}
}

// D-293: with no base definition and no hosted CI, the PR that adds the
// definition runs it, because nothing else can test that PR.
func TestHeadThatAddsLocalCIRunsItWhenNoHostedCIExists(t *testing.T) {
	repo, _ := changeRepo(t)
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(localci.DefinitionPath))); err != nil {
		t.Fatal(err)
	}
	base := changeCommit(t, repo, "remove local CI\n")
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: pytest
    image: python:3.13-slim
    command: python -m pytest
suites:
  - id: test
    checks: [pytest]
`)
	head := changeCommit(t, repo, "add local CI later\n")
	record := Record{Kind: "github", Head: head, Base: base, Branch: "feature", Repo: "owner/repo", RepoPath: repo}
	snap := Snapshot{}
	snap.Pull.Head.SHA, snap.Pull.Head.Repo.FullName = head, record.Repo
	service := &Service{LocalCIRegistry: &requiredCIRegistry{}}
	got, err := service.withRegisteredCI(t.Context(), record, snap, ChangePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if got.localCIConfig == state.LocalCIConfigNotConfigured || got.localCI == nil {
		t.Fatalf("a PR that adds local CI, with no hosted CI, was left unconfigured: config %q, local %+v", got.localCIConfig, got.localCI)
	}
}

// A head whose local CI definition has a field this engine does not know still
// gets its review recorded, while the same CI gate keeps failing for merges.
func TestVerdictIsRecordedWhenHeadDefinitionIsInvalid(t *testing.T) {
	repo, base := changeRepo(t)
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: unit
    image: python:3.13-slim
    command: python -m pytest
    fieldFromNewerEngine: true
suites:
  - id: test
    checks: [unit]
`)
	head := changeCommit(t, repo, "newer definition\n")
	e := newEnv(t)
	e.f.Pull.Head.SHA, e.f.Pull.Head.Repo.FullName = head, "owner/repo"
	e.setCI("success", 1)
	e.write()
	r := e.track()
	recordtest.Write(t, subagent.DefaultStore().Root, namedReviewerID, subagent.Meta{ID: namedReviewerID, Name: "arch-review", RoomID: r.RoomID, Room: e.author.Room})
	policy, err := e.s.RoomChangePolicy(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	policy.Reviewers = []string{namedReviewerID}
	if err = e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if err = e.s.Store.update(func(st *State) error {
		st.Records[r.Key].RepoPath, st.Records[r.Key].Base = repo, base
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.LocalCIRegistry = &requiredCIRegistry{}
	r = e.record()

	snap := Snapshot{}
	snap.Pull.Head.SHA, snap.Pull.Head.Repo.FullName = head, r.Repo
	if _, err := e.s.withRegisteredCI(t.Context(), r, snap, ChangePolicy{}); !errors.Is(err, localci.ErrDefinitionInvalid) {
		t.Fatalf("CI gate err = %v, want ErrDefinitionInvalid, so merges stay blocked", err)
	}
	if err := e.s.RecordReviewResult(t.Context(), r.Key, namedReviewerID, ReviewResult{Verdict: state.VerdictApprove, Head: head, Summary: "Checked code.txt:1"}); err != nil {
		t.Fatalf("verdict was rejected: %v", err)
	}
	got := e.record()
	if got.Verdict != state.VerdictApprove || got.RoundHead != head || got.Round == 0 {
		t.Fatalf("verdict not recorded: verdict=%q roundHead=%q round=%d", got.Verdict, got.RoundHead, got.Round)
	}
}

func TestLocalTrialUsesFrozenBaseForRequiredChecks(t *testing.T) {
	repo, _ := changeRepo(t)
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: unit
    image: python:3.13-slim
    command: python -m pytest
  - id: lint
    image: python:3.13-slim
    command: python -m ruff check
suites:
  - id: test
    checks: [unit, lint]
`)
	base := changeCommit(t, repo, "base definition\n")
	writeTrialDefinition(t, repo, `version: 1
trial: [test]
checks:
  - id: unit
    image: python:3.13-slim
    command: python -m pytest
suites:
  - id: test
    checks: [unit]
`)
	first := changeCommit(t, repo, "first change removes lint\n")
	second := changeCommit(t, repo, "second change\n")
	record := Record{Kind: "local", Key: "second", RepoPath: repo, Base: base, Branch: "feature"}
	service := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}}
	for _, stack := range []bool{false, true} {
		t.Run(fmt.Sprintf("stack=%t", stack), func(t *testing.T) {
			batch := &state.MergeTrial{Base: base, Head: second, Branch: "dyna/merge-trial/1"}
			if stack {
				batch.Head = "another-head"
				batch.Stacks = []state.MergeTrialStack{{Head: second, Branch: "dyna/merge-trial/2"}}
			}
			if err := service.Store.update(func(st *State) error {
				st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(record): batch}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			job, err := service.localTrialJob(record, second)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := service.planRegisteredCI(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			required := plan.required.RequiredChecks(plan.suites)
			if !slices.Contains(required, localci.CheckName("lint")) {
				t.Fatalf("trial with changes %s and %s lost base-required lint: %v", first, second, required)
			}
		})
	}
}

func writeTrialDefinition(t *testing.T, repo, body string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(localci.DefinitionPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
