package prs

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

// Model #1437: the first jobs finish before needs-gated jobs appear in the rollup.
func TestNeedsGatedSuiteSequence(t *testing.T) {
	c, server, at := client(t)
	checks := []fakegithub.Check{{Name: "early", Conclusion: "SUCCESS"}}
	suite := fakegithub.Suite{Status: "IN_PROGRESS", App: "github-actions", CreatedAt: at.Now().Add(-20 * time.Minute), Runs: 1}
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Head: sha})
	for _, step := range []struct {
		name   string
		status string
		checks []fakegithub.Check
		want   contract.PrCI
	}{
		{"early green", "IN_PROGRESS", checks, contract.PrCIPending},
		{"cancelled early job, gated jobs absent", "IN_PROGRESS", append(checks, fakegithub.Check{Name: "cancelled early", Conclusion: "CANCELLED"}), contract.PrCIPending},
		{"lint gated", "IN_PROGRESS", append(checks, fakegithub.Check{Name: "lint", Conclusion: "SUCCESS"}), contract.PrCIPending},
		{"verify finished", "COMPLETED", append(checks, fakegithub.Check{Name: "lint", Conclusion: "SUCCESS"}, fakegithub.Check{Name: "verify", Conclusion: "SUCCESS"}), contract.PrCISuccess},
	} {
		t.Run(step.name, func(t *testing.T) {
			suite.Status = step.status
			server.Edit("owner/repo", 1, func(p *fakegithub.Pull) {
				p.Rollup = "SUCCESS"
				p.Checks = step.checks
				p.Suites = []fakegithub.Suite{suite}
			})
			server.SetCommit("owner/repo", sha, fakegithub.Commit{Rollup: "SUCCESS", Checks: step.checks, Suites: []fakegithub.Suite{suite}})
			assertBothCI(t, c, step.want)
		})
	}
}

// PiG#27: a rerun kept the suite's original creation time but reset it to queued with zero jobs.
// The rollup showed only successful jobs while the rerun was getting started.
func TestRerunOfOldSuiteDoesNotPassBeforeJobsAppear(t *testing.T) {
	c, server, at := client(t)
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Head: sha})
	green := []fakegithub.Check{{Name: "early", Conclusion: "SUCCESS"}}
	for _, step := range []struct {
		name, status, conclusion, workflow, app string
		checks                                  []fakegithub.Check
		runs                                    int
		want                                    contract.PrCI
	}{
		{"cancelled", "COMPLETED", "CANCELLED", "CI", "github-actions", append(slices.Clone(green), fakegithub.Check{Name: "verify", Conclusion: "CANCELLED"}), 2, contract.PrCICancelled},
		{"queued with no jobs", "QUEUED", "", "CI", "github-actions", green, 0, contract.PrCIPending},
		{"queued with app unavailable", "QUEUED", "", "CI", "", green, 0, contract.PrCIPending},
		{"queued with workflow unavailable", "QUEUED", "", "", "github-actions", green, 0, contract.PrCIPending},
		{"in progress with no jobs", "IN_PROGRESS", "", "CI", "github-actions", green, 0, contract.PrCIPending},
		{"finished", "COMPLETED", "SUCCESS", "CI", "github-actions", append(slices.Clone(green), fakegithub.Check{Name: "verify", Conclusion: "SUCCESS"}), 2, contract.PrCISuccess},
	} {
		t.Run(step.name, func(t *testing.T) {
			suites := []fakegithub.Suite{{Status: step.status, Conclusion: step.conclusion, App: step.app, Workflow: step.workflow, CreatedAt: at.Now().Add(-time.Hour), Runs: step.runs}}
			server.Edit("owner/repo", 1, func(p *fakegithub.Pull) {
				p.Rollup, p.Checks, p.Suites = "SUCCESS", step.checks, suites
			})
			server.SetCommit("owner/repo", sha, fakegithub.Commit{Rollup: "SUCCESS", Checks: step.checks, Suites: suites})
			assertBothCI(t, c, step.want)
		})
	}
}

func assertBothCI(t *testing.T, c *Client, want contract.PrCI) {
	t.Helper()
	pulls, err := c.Read(t.Context(), "owner/repo", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if pulls[1].CI != want {
		t.Errorf("PR CI = %s, want %s", pulls[1].CI, want)
	}
	commits, err := c.CommitsCI(t.Context(), "owner/repo", []string{sha})
	if err != nil {
		t.Fatal(err)
	}
	if commits[sha].Result != want {
		t.Errorf("main CI = %s, want %s", commits[sha].Result, want)
	}
}

func TestSuiteSettlementControls(t *testing.T) {
	for _, test := range []struct {
		name   string
		age    time.Duration
		runs   int
		status string
		total  int
		want   contract.PrCI
	}{
		{"old ghost", 10*time.Minute + time.Second, 0, "QUEUED", 1, contract.PrCISuccess},
		{"fresh ghost", 9 * time.Minute, 0, "QUEUED", 1, contract.PrCIPending},
		{"exact ten minutes", 10 * time.Minute, 0, "QUEUED", 1, contract.PrCIPending},
		{"old active", time.Hour, 1, "IN_PROGRESS", 1, contract.PrCIPending},
		{"old in progress without jobs", time.Hour, 0, "IN_PROGRESS", 1, contract.PrCIPending},
		{"completed", time.Minute, 1, "COMPLETED", 1, contract.PrCISuccess},
		{"unread suite", time.Hour, 0, "COMPLETED", 2, contract.PrCIPending},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, server, at := client(t)
			suites := []fakegithub.Suite{{Status: test.status, CreatedAt: at.Now().Add(-test.age), Runs: test.runs}}
			checks := []fakegithub.Check{{Name: "early", Conclusion: "SUCCESS"}}
			server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Head: sha, Rollup: "SUCCESS", Checks: checks, Suites: suites, SuiteTotal: test.total})
			server.SetCommit("owner/repo", sha, fakegithub.Commit{Rollup: "SUCCESS", Checks: checks, Suites: suites, SuiteTotal: test.total})
			assertBothCI(t, c, test.want)
		})
	}
}

// Model horizon#2527: GitHub failed to start a workflow's jobs, so the rollup held only skipped contexts and read SUCCESS,
// and a second workflow failed with no check runs at all; each suite's conclusion was FAILURE.
func TestFailedSuiteFailsCIWhenContextsReadGreen(t *testing.T) {
	c, server, at := client(t)
	checks := []fakegithub.Check{{Name: "book", Conclusion: "SUCCESS"}, {Name: "lint", Conclusion: "SKIPPED"}, {Name: "verify", Conclusion: "SKIPPED"}}
	suites := []fakegithub.Suite{
		{Status: "COMPLETED", Conclusion: "SUCCESS", App: "github-actions", CreatedAt: at.Now(), Runs: 1, Workflow: "modding-book"},
		{Status: "COMPLETED", Conclusion: "FAILURE", App: "github-actions", CreatedAt: at.Now(), Runs: 2, Workflow: "dyna-checks"},
		{Status: "COMPLETED", Conclusion: "FAILURE", App: "github-actions", CreatedAt: at.Now(), Workflow: "launcher-windows-check"},
		{Status: "QUEUED", App: "greptile-apps", CreatedAt: at.Now().Add(-time.Hour)},
	}
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Head: sha, Rollup: "SUCCESS", Checks: checks, Suites: suites})
	server.SetCommit("owner/repo", sha, fakegithub.Commit{Rollup: "SUCCESS", Checks: checks, Suites: suites})
	assertBothCI(t, c, contract.PrCIFailure)
	pulls, err := c.Read(t.Context(), "owner/repo", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pulls[1].FailedChecks, []string{"dyna-checks (workflow run failed)", "launcher-windows-check (workflow run failed)"}; !slices.Equal(got, want) {
		t.Errorf("failed checks = %q, want %q", got, want)
	}
}

// A hasNextPage flag is authoritative even if totalCount temporarily disagrees.
func TestCISummaryDoesNotSettlePartialPages(t *testing.T) {
	for _, raw := range []string{
		`{"suites":{"totalCount":0,"pageInfo":{"hasNextPage":true}},"rollup":{"state":"SUCCESS","contexts":{"totalCount":1,"nodes":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}}}`,
		`{"suites":{},"rollup":{"state":"SUCCESS","contexts":{"totalCount":1,"pageInfo":{"hasNextPage":true},"nodes":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}}}`,
		`{"suites":{},"rollup":{"state":"SUCCESS","contexts":{"totalCount":2,"nodes":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}}}`,
	} {
		var answer struct {
			Suites gqlSuites
			Rollup *gqlRollup
		}
		if err := json.Unmarshal([]byte(raw), &answer); err != nil {
			t.Fatal(err)
		}
		for _, rule := range []ciRule{ciPullHead, ciMainHead} {
			if got, _ := summarizeCI(answer.Rollup, answer.Suites, rule, 0, time.Now()); got != contract.PrCIPending {
				t.Errorf("partial page settled as %s: %s", got, raw)
			}
		}
	}
}

func TestVisibleFailureSettlesIncompleteCI(t *testing.T) {
	for _, typ := range []string{"CheckRun", "StatusContext"} {
		for _, truncated := range []bool{false, true} {
			var rollup gqlRollup
			raw := `{"state":"FAILURE","contexts":{"totalCount":2,"nodes":[{"__typename":"CheckRun","name":"gate","status":"IN_PROGRESS"},{"__typename":"` + typ + `","name":"lint","context":"lint","status":"COMPLETED","conclusion":"FAILURE","state":"ERROR"}]}}`
			if err := json.Unmarshal([]byte(raw), &rollup); err != nil {
				t.Fatal(err)
			}
			rollup.Contexts.PageInfo.HasNextPage = truncated
			var suites gqlSuites
			if err := json.Unmarshal([]byte(`{"totalCount":2,"nodes":[{"status":"IN_PROGRESS"}]}`), &suites); err != nil {
				t.Fatal(err)
			}
			for _, rule := range []ciRule{ciPullHead, ciMainHead} {
				got, names := summarizeCI(&rollup, suites, rule, 0, time.Now())
				if got != contract.PrCIFailure || !slices.Equal(names, []string{"lint"}) {
					t.Fatalf("%s %v: %s %v", typ, truncated, got, names)
				}
			}
		}
	}
}

func TestEmptyFailureRollupWaitsForPendingSuite(t *testing.T) {
	var suites gqlSuites
	if err := json.Unmarshal([]byte(`{"totalCount":1,"nodes":[{"status":"IN_PROGRESS"}]}`), &suites); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"FAILURE", "ERROR"} {
		for _, rule := range []ciRule{ciPullHead, ciMainHead} {
			got, _ := summarizeCI(&gqlRollup{State: state}, suites, rule, 0, time.Now())
			if got != contract.PrCIPending {
				t.Fatalf("%s %d: %s", state, rule, got)
			}
		}
	}
}

func TestFailedSuiteOverridesEmptyAggregateAndPendingSuites(t *testing.T) {
	for _, raw := range []string{
		`{"totalCount":2,"nodes":[{"status":"IN_PROGRESS"},{"status":"COMPLETED","conclusion":"FAILURE","app":{"slug":"actions"}}]}`,
		`{"totalCount":2,"pageInfo":{"hasNextPage":true},"nodes":[{"status":"COMPLETED","conclusion":"FAILURE","app":{"slug":"actions"}}]}`,
	} {
		var suites gqlSuites
		if err := json.Unmarshal([]byte(raw), &suites); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"FAILURE", "ERROR"} {
			for _, rule := range []ciRule{ciPullHead, ciMainHead} {
				got, names := summarizeCI(&gqlRollup{State: state}, suites, rule, 0, time.Now())
				if got != contract.PrCIFailure || !slices.Equal(names, []string{"actions (workflow run failed)"}) {
					t.Fatalf("%s %d: %s %v", state, rule, got, names)
				}
			}
		}
	}
}

// A run dispatched on main, such as one gating a PR, is filed under main's head but is not main's CI (horizon, 2026-11-13);
// runs started by the branch itself (push, schedule, workflow_run) still are.
func TestMainCILeavesOutDispatchedRuns(t *testing.T) {
	c, server, _ := client(t)
	push := fakegithub.Check{Name: "build", Conclusion: "SUCCESS", Event: "push"}
	gate := fakegithub.Check{Name: "gate pr 7", Conclusion: "FAILURE", Event: "workflow_dispatch"}
	suites := []fakegithub.Suite{{Status: "COMPLETED", Conclusion: "SUCCESS", Workflow: "ci", Runs: 1}, {Status: "COMPLETED", Conclusion: "FAILURE", Workflow: "dyna-ci", Event: "workflow_dispatch", Runs: 1}}
	for _, step := range []struct {
		name   string
		checks []fakegithub.Check
		want   contract.PrCI
	}{
		{"push green, dispatch red", []fakegithub.Check{push, gate}, contract.PrCISuccess},
		{"only a dispatched run", []fakegithub.Check{gate}, contract.PrCINone},
		{"push red", []fakegithub.Check{{Name: "build", Conclusion: "FAILURE", Event: "push"}, gate}, contract.PrCIFailure},
		{"chained run red", []fakegithub.Check{push, gate, {Name: "deploy", Conclusion: "FAILURE", Event: "workflow_run"}}, contract.PrCIFailure},
		{"chained run still going", []fakegithub.Check{push, gate, {Name: "deploy", Status: "IN_PROGRESS", Event: "workflow_run"}}, contract.PrCIPending},
		{"scheduled run counts", []fakegithub.Check{push, {Name: "nightly", Conclusion: "FAILURE", Event: "schedule"}}, contract.PrCIFailure},
		{"API-dispatched run", []fakegithub.Check{push, {Name: "gate pr 7", Conclusion: "FAILURE", Event: "repository_dispatch"}}, contract.PrCISuccess},
	} {
		t.Run(step.name, func(t *testing.T) {
			server.SetCommit("owner/repo", sha, fakegithub.Commit{Rollup: "FAILURE", Checks: step.checks, Suites: suites})
			commits, err := c.CommitsCI(t.Context(), "owner/repo", []string{sha})
			if err != nil {
				t.Fatal(err)
			}
			got := commits[sha]
			if got.Result != step.want || slices.ContainsFunc(got.Runs, func(r contract.CIRun) bool { return r.Name == gate.Name }) {
				t.Fatalf("%+v, want %s without the dispatched run", got, step.want)
			}
		})
	}
}

// Model horizon sunrise#3932: a conflicting PR runs no pull_request workflows, and its head's only check was a skipped job.
// Skipped contexts alone are no CI, never a pass; one that ran beside them still decides.
func TestOnlySkippedChecksAreNoCI(t *testing.T) {
	suites := []fakegithub.Suite{{Status: "COMPLETED", Conclusion: "SKIPPED", App: "github-actions", Workflow: "proof", Runs: 1}}
	for _, step := range []struct {
		name   string
		checks []fakegithub.Check
		want   contract.PrCI
	}{
		{"only skipped", []fakegithub.Check{{Name: "Proof", Conclusion: "SKIPPED"}}, contract.PrCINone},
		{"skipped beside a pass", []fakegithub.Check{{Name: "Proof", Conclusion: "SKIPPED"}, {Name: "build", Conclusion: "SUCCESS"}}, contract.PrCISuccess},
		{"skipped beside a neutral run", []fakegithub.Check{{Name: "Proof", Conclusion: "SKIPPED"}, {Name: "scan", Conclusion: "NEUTRAL"}}, contract.PrCISuccess},
	} {
		t.Run(step.name, func(t *testing.T) {
			c, server, at := client(t)
			s := slices.Clone(suites)
			s[0].CreatedAt = at.Now()
			server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Head: sha, Rollup: "SUCCESS", Checks: step.checks, Suites: s})
			server.SetCommit("owner/repo", sha, fakegithub.Commit{Rollup: "SUCCESS", Checks: step.checks, Suites: s})
			assertBothCI(t, c, step.want)
		})
	}
}
