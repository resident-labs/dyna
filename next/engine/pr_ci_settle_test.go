package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

// ciFinished returns the github.ci.finished events of every tracked PR, in order.
func (r *prRig) ciFinished(t *testing.T) []contract.CIFinished {
	t.Helper()
	rows, err := r.s.store.Read().QueryContext(r.ctx, "SELECT body FROM events WHERE type='github.ci.finished' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []contract.CIFinished
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		var body contract.CIFinished
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		out = append(out, body)
	}
	return out
}

// ciWakes counts the CI kinds the owner was woken with.
func (r *prRig) ciWakes(t *testing.T, kind contract.PrChangeKind) int {
	t.Helper()
	count := 0
	for _, wake := range r.wakes(t, r.child) {
		for _, change := range wake.Changes {
			if slices.Contains(change.Kinds, kind) {
				count++
			}
		}
	}
	return count
}

// requireCI checks the stored CI of the PR, which the badge, pr status and dyna_holds read.
func (r *prRig) requireCI(t *testing.T, want contract.PrCI) {
	t.Helper()
	row, err := r.s.store.PR(r.ctx, "owner/repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	if row.CI != want {
		t.Fatalf("stored CI %q, want %q", row.CI, want)
	}
}

func (r *prRig) setChecks(rollup string, checks ...fakegithub.Check) {
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Rollup, p.Checks, p.FailedChecks = rollup, checks, nil })
}

func TestPRCIWaitsForEveryContextAndReportsCancellation(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "FAILURE", Checks: []fakegithub.Check{
		{Name: "verify"}, {Name: "build linux", Status: "QUEUED"}, {Name: "build macos-x64", Conclusion: "CANCELLED"}, {Name: "lint", Conclusion: "SUCCESS"}}})
	for i := 0; i < 3; i++ {
		r.pass(t)
	}
	if got := r.ciFinished(t); len(got) != 0 || r.ciWakes(t, contract.PrChangeCIFailed) != 0 {
		t.Fatalf("a cancelled check told a result while others ran: %+v", got)
	}
	r.requireCI(t, contract.PrCIPending)
	r.setChecks("FAILURE", fakegithub.Check{Name: "verify", Conclusion: "SUCCESS"}, fakegithub.Check{Name: "build linux", Conclusion: "SUCCESS"}, fakegithub.Check{Name: "build macos-x64", Conclusion: "CANCELLED"}, fakegithub.Check{Name: "lint", Conclusion: "NEUTRAL"})
	r.pass(t)
	r.pass(t)
	got := r.ciFinished(t)
	if len(got) != 1 || got[0].Result != contract.PrCICancelled {
		t.Fatalf("every check done and cancellation reported: %+v", got)
	}
	r.requireCI(t, contract.PrCICancelled)
	if r.ciWakes(t, contract.PrChangeCICancelled) != 1 || r.ciWakes(t, contract.PrChangeCIFailed) != 0 || r.ciWakes(t, contract.PrChangeCIPassed) != 0 {
		t.Fatal("cancellation must wake once, never as failure or pass")
	}
	status, fail := r.s.PrStatus(r.ctx, sessionCaller(r.child), contract.PrStatusRequest{Repo: "owner/repo", Number: 1})
	if fail != nil || !slices.Equal(status.PR.CancelledChecks, []string{"build macos-x64"}) {
		t.Fatalf("persisted status: %+v %v", status, fail)
	}
}

func TestPRCIFailureWakesTheOwnerOnceWhenEveryContextIsDone(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "FAILURE", Checks: []fakegithub.Check{
		{Name: "verify", Conclusion: "FAILURE"}, {Name: "build linux", Status: "IN_PROGRESS"}, {Name: "build macos-x64", Conclusion: "CANCELLED"}}})
	for i := 0; i < 3; i++ {
		r.pass(t)
	}
	if got := r.ciFinished(t); len(got) != 0 || r.ciWakes(t, contract.PrChangeCIFailed) != 0 {
		t.Fatalf("a failed check told before the run was complete: %+v", got)
	}
	r.requireCI(t, contract.PrCIPending)
	r.setChecks("FAILURE", fakegithub.Check{Name: "verify", Conclusion: "FAILURE"}, fakegithub.Check{Name: "build linux", Conclusion: "SUCCESS"}, fakegithub.Check{Name: "build macos-x64", Conclusion: "CANCELLED"})
	r.pass(t)
	r.pass(t)
	got := r.ciFinished(t)
	if len(got) != 1 || got[0].Result != contract.PrCIFailure || r.ciWakes(t, contract.PrChangeCIFailed) != 1 {
		t.Fatalf("one failure once complete: %+v, wakes %d", got, r.ciWakes(t, contract.PrChangeCIFailed))
	}
	for _, wake := range r.wakes(t, r.child) {
		for _, change := range wake.Changes {
			if change.FailedChecks != nil && !slices.Equal(change.FailedChecks, []string{"verify"}) {
				t.Fatalf("failed checks %v, want only the failed one", change.FailedChecks)
			}
		}
	}
	r.requireCI(t, contract.PrCIFailure)
}

func TestPRCIWithEveryContextCancelledIsCancelled(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "build", Conclusion: "CANCELLED"}, {Name: "test", Conclusion: "CANCELLED"}}})
	r.pass(t)
	if got := r.ciFinished(t); len(got) != 1 || got[0].Result != contract.PrCICancelled || r.ciWakes(t, contract.PrChangeCICancelled) != 1 {
		t.Fatalf("nothing ran and passed: %+v", got)
	}
}

func TestPRCIPendingStatusContextHoldsTheHead(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}, {Name: "ci/external", Conclusion: "PENDING", StatusContext: true}}})
	for i := 0; i < 3; i++ {
		r.pass(t)
	}
	if got := r.ciFinished(t); len(got) != 0 {
		t.Fatalf("a pending status told a result: %+v", got)
	}
	r.requireCI(t, contract.PrCIPending)
	r.setChecks("SUCCESS", fakegithub.Check{Name: "build", Conclusion: "SUCCESS"}, fakegithub.Check{Name: "ci/external", Conclusion: "SUCCESS", StatusContext: true})
	r.pass(t)
	if got := r.ciFinished(t); len(got) != 1 || got[0].Result != contract.PrCISuccess {
		t.Fatalf("got %+v", got)
	}
}

func TestPRCIWithNoContextKeepsItsRules(t *testing.T) {
	for name, test := range map[string]struct {
		pull fakegithub.Pull
		want contract.PrCI
	}{
		"success with no check": {fakegithub.Pull{Rollup: "SUCCESS", NoChecks: true}, contract.PrCINone},
		"pending with no check": {fakegithub.Pull{Rollup: "PENDING", NoChecks: true}, contract.PrCIPending},
		"no rollup":             {fakegithub.Pull{}, contract.PrCINone},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPRRig(t)
			test.pull.Repo, test.pull.Number = "owner/repo", 1
			r.open(t, test.pull)
			r.pass(t)
			if got := r.ciFinished(t); len(got) != 0 {
				t.Fatalf("got %+v", got)
			}
			r.requireCI(t, test.want)
		})
	}
}

// Unread contexts cannot be proven terminal, regardless of the rollup state.
func TestPRCITruncatedReadStaysPending(t *testing.T) {
	var checks []fakegithub.Check
	for i := 0; i < 50; i++ {
		checks = append(checks, fakegithub.Check{Name: fmt.Sprintf("check-%d", i), Conclusion: "SUCCESS"})
	}
	for rollup, want := range map[string]contract.PrCI{"SUCCESS": contract.PrCIPending, "FAILURE": contract.PrCIPending, "PENDING": contract.PrCIPending} {
		t.Run(rollup, func(t *testing.T) {
			r := newPRRig(t)
			r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: rollup, Checks: checks, Total: 120})
			r.pass(t)
			r.requireCI(t, want)
			if got := len(r.ciFinished(t)); (want == contract.PrCIPending) != (got == 0) {
				t.Fatalf("%d results told for %q", got, want)
			}
		})
	}
}
