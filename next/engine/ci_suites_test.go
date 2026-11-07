package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

// Root's live store read: horizon #1437, head 77ccda3fb653b32db715569d2e427d9fa17be03f,
// github.ci.finished seq 155444 at 2026-11-06 16:16:46 UTC reported success.
// Run 42250817563: lint ended at 16:26:29 UTC and verify at 16:29:20 UTC,
// both after that event (job timestamps independently read from GitHub).
// Reproduce the early all-visible-green window, then finish the gated jobs.
func TestNeedsGatedSuitesHoldPRAndMainEvents(t *testing.T) {
	r := mainRig(t)
	checks := []fakegithub.Check{{Name: "early", Conclusion: "SUCCESS"}}
	suite := fakegithub.Suite{Status: "IN_PROGRESS", App: "github-actions", CreatedAt: r.clock.Now().Add(-20 * time.Minute), Runs: 1}
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "SUCCESS", Checks: checks, Suites: []fakegithub.Suite{suite}})
	r.moveMain("owner/repo", "gated", fakegithub.Commit{Rollup: "SUCCESS", Checks: checks, Suites: []fakegithub.Suite{suite}})
	r.next(t)
	if got := r.ciFinished(t); len(got) != 0 {
		t.Errorf("PR settled before gated jobs: %+v", got)
	}
	if got := r.mainCIs(t); len(got) != 0 {
		t.Errorf("main settled before gated jobs: %+v", got)
	}
	r.requireCI(t, contract.PrCIPending)
	checks = append(checks, fakegithub.Check{Name: "lint", Conclusion: "SUCCESS"})
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Checks = checks })
	r.github.SetCommit("owner/repo", "gated", fakegithub.Commit{Rollup: "SUCCESS", Checks: checks, Suites: []fakegithub.Suite{suite}})
	r.next(t)
	r.requireCI(t, contract.PrCIPending)
	if len(r.ciFinished(t)) != 0 || len(r.mainCIs(t)) != 0 {
		t.Fatal("settled before verify")
	}
	checks = append(checks, fakegithub.Check{Name: "verify", Conclusion: "SUCCESS"})
	suite.Status = "COMPLETED"
	suite.Runs = 3
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Checks = checks; p.Suites = []fakegithub.Suite{suite} })
	r.github.SetCommit("owner/repo", "gated", fakegithub.Commit{Rollup: "SUCCESS", Checks: checks, Suites: []fakegithub.Suite{suite}})
	r.next(t)
	r.next(t)
	r.requireCI(t, contract.PrCISuccess)
	if got := r.ciFinished(t); len(got) != 1 || got[0].Result != contract.PrCISuccess {
		t.Fatalf("PR completion: %+v", got)
	}
	if got := r.mainCIs(t); len(got) != 1 || got[0].Result != contract.PrCISuccess {
		t.Fatalf("main completion: %+v", got)
	}
}
