package watch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

func TestLocalCancellationAggregateKeepsRealFailures(t *testing.T) {
	// GitHub may omit a suite ID; the saved cancelled run itself is still ours.
	withoutSuite := withoutLocalHostedCancellations(
		Snapshot{Runs: []Run{{ID: 10, Attempt: 1, Status: "completed", Conclusion: "cancelled"}}},
		localci.HostedRuns{Attempts: map[int64]int{10: 1}}, watchDynaDefinition(t),
	)
	if len(withoutSuite.Runs) != 0 {
		t.Fatal("saved cancelled run without a suite was retained")
	}
	for _, tc := range []struct {
		name, runStatus, conclusion, dependency string
		savedAttempt                            int
		want                                    CIVerdict
	}{
		{"aggregate while run active", "in_progress", "", "cancelled", 2, "passed"},
		{"aggregate finishes failed", "completed", "failure", "cancelled", 2, "passed"},
		{"cancelled run contains real failure", "completed", "cancelled", "failure", 2, "failed"},
		{"failed run contains real failure", "completed", "failure", "failure", 2, "failed"},
		{"unrecorded cancellation", "completed", "failure", "cancelled", 0, "failed"},
		{"new attempt", "completed", "failure", "cancelled", 1, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := Run{ID: 10, Attempt: 2, CheckSuiteID: 20, Path: ".github/workflows/ci.yml", Status: tc.runStatus, Conclusion: tc.conclusion}
			snap := Snapshot{Runs: []Run{run}}
			for i, name := range []string{"go-test-shard (0)", "go-static", "frontend", "frontend-check", "go-test"} {
				c := actionsCheck(Check{ID: int64(i + 1), Name: name, Status: "completed", Conclusion: "success"})
				c.CheckSuite.ID = 20
				if i == 0 {
					c.Conclusion = tc.dependency
				}
				if name == "go-test" {
					c.Conclusion = "failure"
				}
				snap.Checks = append(snap.Checks, c)
			}
			saved := localci.HostedRuns{Attempts: map[int64]int{}}
			if tc.savedAttempt != 0 {
				saved.Attempts[10] = tc.savedAttempt
			}
			filtered := withoutLocalHostedCancellations(snap, saved, watchDynaDefinition(t))
			state, key, jobs, runs := (&Service{}).registeredHostedStatus(filtered, Record{}, ChangePolicy{})
			if tc.want == "passed" && state != "pending" {
				t.Fatalf("cancelled suite supplied a conclusive result: %s", state)
			}
			got := chooseRegisteredCI("head", state, key, jobs, runs, "passed", nil, "")
			if got.Status != tc.want {
				t.Fatalf("CI = %s, want %s (hosted %s)", got.Status, tc.want, state)
			}
			if len(snap.Runs) != 1 || len(snap.Checks) != 5 {
				t.Fatal("filter changed input snapshot")
			}
		})
	}
}

func TestLocalCancellationAggregateResumesAfterInfrastructureError(t *testing.T) {
	for _, dependency := range []string{"cancelled", "failure"} {
		t.Run(dependency, func(t *testing.T) {
			repo, head := changeRepo(t)
			run := Run{ID: 10, Attempt: 1, CheckSuiteID: 20, Path: ".github/workflows/ci.yml", HeadSHA: head, HeadBranch: "feature", Event: "pull_request", Status: "completed", Conclusion: "failure"}
			registry := &hostedCIRegistry{hosted: localci.HostedRuns{IDs: []int64{10}, Attempts: map[int64]int{10: 1}}}
			s := &Service{LocalCIRegistry: registry}
			reruns := 0
			s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
				endpoint := ""
				for _, arg := range cmd.Args {
					if strings.HasPrefix(arg, "repos/") {
						endpoint = arg
					}
				}
				switch {
				case strings.HasSuffix(endpoint, "/runs/10"):
					return writeFakeJSON(cmd.Stdout, run)
				case strings.Contains(endpoint, "/attempts/1/jobs?"):
					var jobs []budgetJob
					for _, name := range []string{"go-test-shard (0)", "go-static", "frontend", "frontend-check", "go-test"} {
						j := budgetJob{Name: name, Status: "completed", Conclusion: "success"}
						if name == "go-test-shard (0)" {
							j.Conclusion = dependency
						}
						if name == "go-test" {
							j.Conclusion = "failure"
						}
						jobs = append(jobs, j)
					}
					return writeFakeJSON(cmd.Stdout, jobsResponse{Jobs: jobs})
				case strings.HasSuffix(endpoint, "/rerun"):
					reruns++
					return writeFakeJSON(cmd.Stdout, emptyResponse{})
				default:
					return fmt.Errorf("unexpected endpoint %s", endpoint)
				}
			}
			job := registeredCIJob{record: Record{Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", RepoPath: repo}, head: head, branch: "feature"}
			if err := s.resumeHostedCI(context.Background(), job, "repo"); err != nil {
				t.Fatal(err)
			}
			want := 0
			if dependency == "cancelled" {
				want = 1
			}
			if reruns != want {
				t.Fatalf("reruns = %d, want %d", reruns, want)
			}
		})
	}
}

type cancellationCIRegistry struct {
	requiredCIRegistry
	mode string
}

func (r *cancellationCIRegistry) Mode(context.Context, string) (string, error) { return r.mode, nil }

// PR facts and the final trial publication gate must use the same normalized
// hosted evidence, including while the cancelled workflow remains active.
func TestLocalCancellationReachesFactsAndTrialGate(t *testing.T) {
	ctx := context.Background()
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "trial\n")
	common, err := localci.CanonicalRepo(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	branch := "dyna/merge-trial/1"
	r := Record{Kind: "github", Key: "pr", Head: head, Base: base, Host: "github.com", Repo: "owner/repo", RepoPath: repo, Branch: branch}
	snap := Snapshot{Runs: []Run{{ID: 10, Attempt: 1, CheckSuiteID: 20, Path: ".github/workflows/ci.yml", HeadSHA: head, HeadBranch: branch, Event: "push", Status: "in_progress"}}}
	snap.Pull.Head.SHA, snap.Pull.Head.Repo.FullName = head, r.Repo
	for i, name := range []string{"go-test-shard (0)", "go-static", "frontend", "frontend-check", "go-test"} {
		c := actionsCheck(Check{ID: int64(i + 1), Name: name, Status: "completed", Conclusion: "success"})
		c.CheckSuite.ID = 20
		if i == 0 {
			c.Conclusion = "cancelled"
		}
		if name == "go-test" {
			c.Conclusion = "failure"
		}
		snap.Checks = append(snap.Checks, c)
	}
	for _, mode := range []string{localci.ModeLocal, localci.ModeBoth} {
		for _, tc := range []struct {
			state localci.Outcome
			want  CIVerdict
		}{
			{localci.OutcomePassed, VerdictPassed},
			{localci.OutcomeFailed, VerdictFailed},
			// A saved result with no outcome yet waits.
			{"", VerdictPending},
		} {
			t.Run(mode+"/"+string(tc.want), func(t *testing.T) {
				result := localci.Result{Repo: common, SHA: head, Node: "node", Status: tc.state}
				for _, name := range watchDynaRequiredChecks(t) {
					result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: tc.state})
				}
				registry := &cancellationCIRegistry{mode: mode, requiredCIRegistry: requiredCIRegistry{result: result, hostedCIRegistry: hostedCIRegistry{hosted: localci.HostedRuns{IDs: []int64{10}, Attempts: map[int64]int{10: 1}}}}}
				s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}, LocalCIRegistry: registry}
				if err := s.Store.update(func(st *State) error {
					st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Base: base, Head: head, Branch: branch}}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
					endpoint := ""
					for _, arg := range cmd.Args {
						if strings.HasPrefix(arg, "repos/") {
							endpoint = arg
						}
					}
					switch {
					case strings.Contains(endpoint, "/check-runs?"):
						return writeFakeJSON(cmd.Stdout, checkRunsResponse{CheckRuns: snap.Checks})
					case strings.Contains(endpoint, "/actions/runs?"):
						return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: snap.Runs})
					case strings.Contains(endpoint, "/statuses?"):
						return writeFakeJSON(cmd.Stdout, []Status{})
					default:
						return fmt.Errorf("unexpected endpoint %s", endpoint)
					}
				}
				normalized, err := s.withRegisteredCI(ctx, r, snap, ChangePolicy{})
				if err != nil {
					t.Fatal(err)
				}
				if facts := normalized.GitHubFacts(r); facts.CI != string(tc.want) {
					t.Fatalf("PR facts = %s, want %s", facts.CI, tc.want)
				}
				trial, err := s.mergeTrialCI(ctx, r, branch, head)
				if err != nil {
					t.Fatal(err)
				}
				if trial.Status != tc.want {
					t.Fatalf("trial publication gate = %s, want %s", trial.Status, tc.want)
				}
			})
		}
	}
}
