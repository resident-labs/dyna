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
			if got, _ := summarizeCI(answer.Rollup, answer.Suites, rule, time.Now()); got != contract.PrCIPending {
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
				got, names := summarizeCI(&rollup, suites, rule, time.Now())
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
			got, _ := summarizeCI(&gqlRollup{State: state}, suites, rule, time.Now())
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
				got, names := summarizeCI(&gqlRollup{State: state}, suites, rule, time.Now())
				if got != contract.PrCIFailure || !slices.Equal(names, []string{"actions (workflow run failed)"}) {
					t.Fatalf("%s %d: %s %v", state, rule, got, names)
				}
			}
		}
	}
}
