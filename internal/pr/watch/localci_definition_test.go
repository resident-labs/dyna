package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
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
	plan, err := (&Service{}).planRegisteredCI(t.Context(), registeredCIJob{record: record, head: head, base: base, trial: true})
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
	observation := (&Service{}).registeredLocalObservation(record, Snapshot{}, ChangePolicy{}, plan, result)
	if observation.Status != VerdictPending || !slices.Contains(observation.Jobs, "lint") {
		t.Fatalf("head removed a base check and passed: %+v", observation)
	}
	result.Checks = append(result.Checks, localci.SuiteResult{CheckName: "lint", Status: localci.OutcomePassed})
	observation = (&Service{}).registeredLocalObservation(record, Snapshot{}, ChangePolicy{}, plan, result)
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
	if got.localCIConfig != localCIConfigNotConfigured || got.localCI != nil {
		t.Fatalf("unconfigured repository acquired local CI: %+v", got.localCI)
	}
	if state, _, _, _ := got.CIStatus(); state != "passed" {
		t.Fatalf("hosted CI did not remain authoritative: %s", state)
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
