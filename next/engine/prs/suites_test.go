package prs

import (
	"encoding/json"
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
