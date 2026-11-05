package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

// mainCIs lists the git.main_ci_finished events of every room, oldest first.
func (r *prRig) mainCIs(t *testing.T) []contract.GitMainCIFinished {
	t.Helper()
	rows, err := r.s.store.Read().QueryContext(r.ctx, "SELECT body FROM events WHERE type='git.main_ci_finished' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []contract.GitMainCIFinished
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		var body contract.GitMainCIFinished
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		out = append(out, body)
	}
	return out
}

// mainRig is a rig whose repository has read its first head, so the next head is a move.
func mainRig(t *testing.T) *prRig {
	t.Helper()
	r := newPRRig(t)
	r.pass(t)
	return r
}

// next moves a minute on and passes, and returns the GraphQL queries the pass made.
func (r *prRig) next(t *testing.T) int {
	t.Helper()
	r.clock.add(time.Minute)
	before, _, _ := r.github.Counts()
	r.pass(t)
	after, _, _ := r.github.Counts()
	return after - before
}

func TestMainCIFinishedFiresOncePerHeadAcrossARestart(t *testing.T) {
	r := mainRig(t)
	r.github.SetMain("owner/repo", "m1")
	r.github.SetCommit("owner/repo", "m1", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS", URL: "https://x/1"}, {Name: "test", Conclusion: "SUCCESS", URL: "https://x/2"}}})
	r.next(t)
	want := contract.GitMainCIFinished{Repo: "owner/repo", Branch: "main", Sha: "m1", Result: contract.PrCISuccess, Runs: []contract.CIRun{{Name: "build", Conclusion: "SUCCESS", URL: "https://x/1"}, {Name: "test", Conclusion: "SUCCESS", URL: "https://x/2"}}}
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Repo != want.Repo || got[0].Branch != want.Branch || got[0].Sha != want.Sha || got[0].Result != want.Result || len(got[0].Runs) != 2 || got[0].Runs[1] != want.Runs[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if queries := r.next(t); queries != 0 {
		t.Errorf("a reported head cost %d queries", queries)
	}
	r.connect()
	r.next(t)
	if got := r.mainCIs(t); len(got) != 1 {
		t.Fatalf("a restart reported the head again: %+v", got)
	}
}

func TestMainCIFinishedWaitsForARunningCI(t *testing.T) {
	r := mainRig(t)
	r.github.SetMain("owner/repo", "m1")
	r.github.SetCommit("owner/repo", "m1", fakegithub.Commit{Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "build"}}})
	for i := 0; i < 3; i++ {
		if queries := r.next(t); queries != 1 {
			t.Fatalf("pass %d made %d queries while CI ran, want 1", i, queries)
		}
	}
	if got := r.mainCIs(t); len(got) != 0 {
		t.Fatalf("a running CI fired %+v", got)
	}
	r.github.SetCommit("owner/repo", "m1", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}}})
	r.next(t)
	if got := r.mainCIs(t); len(got) != 1 || got[0].Result != contract.PrCISuccess {
		t.Fatalf("got %+v", got)
	}
}

func TestMainCIFinishedFailureHasTheFailedRun(t *testing.T) {
	r := mainRig(t)
	r.github.SetMain("owner/repo", "m1")
	r.github.SetCommit("owner/repo", "m1", fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "test", Conclusion: "FAILURE", URL: "https://x/3"}}})
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Result != contract.PrCIFailure || len(got[0].Runs) != 1 || got[0].Runs[0].Conclusion != "FAILURE" {
		t.Fatalf("got %+v", got)
	}
}

func TestMainCIFinishedIsForTheNewestHeadOnly(t *testing.T) {
	r := mainRig(t)
	r.github.SetMain("owner/repo", "m1")
	r.github.SetCommit("owner/repo", "m1", fakegithub.Commit{Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "build"}}})
	r.next(t)
	r.github.SetMain("owner/repo", "m2")
	r.github.SetCommit("owner/repo", "m2", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}}})
	r.next(t)
	r.github.SetCommit("owner/repo", "m1", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}}})
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Sha != "m2" {
		t.Fatalf("got %+v, want one event for m2", got)
	}
}

func TestMainCIFinishedIsNotToldForARepositoryWithNoCI(t *testing.T) {
	for name, commit := range map[string]fakegithub.Commit{
		"no rollup":             {},
		"success with no check": {Rollup: "SUCCESS"},
	} {
		t.Run(name, func(t *testing.T) {
			r := mainRig(t)
			r.github.SetMain("owner/repo", "m1")
			r.github.SetCommit("owner/repo", "m1", commit)
			for i := 0; i < 10; i++ {
				if queries := r.next(t); queries != 1 {
					t.Fatalf("pass %d made %d queries inside the grace, want 1", i, queries)
				}
			}
			r.next(t)
			for i := 0; i < 3; i++ {
				if queries := r.next(t); queries != 0 {
					t.Fatalf("a head with no CI still costs %d queries after the grace", queries)
				}
			}
			if got := r.mainCIs(t); len(got) != 0 {
				t.Fatalf("no CI fired %+v", got)
			}
		})
	}
}

func TestMainCIFinishedDoesNotBackfillTheFirstHead(t *testing.T) {
	r := newPRRig(t)
	r.github.SetCommit("owner/repo", "base", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}}})
	r.pass(t)
	if queries := r.next(t); queries != 0 {
		t.Errorf("the first head cost %d queries", queries)
	}
	if got := r.mainCIs(t); len(got) != 0 {
		t.Fatalf("the first head was reported: %+v", got)
	}
}

func TestMainCIFinishedCoversARepositoryThatIsNotTheRoomDefault(t *testing.T) {
	r := newPRRig(t)
	if err := r.s.store.AddRoomRepo(r.ctx, r.room, "owner/other", false); err != nil {
		t.Fatal(err)
	}
	r.pass(t)
	r.github.SetMain("owner/other", "o1")
	r.github.SetCommit("owner/other", "o1", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}}})
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Repo != "owner/other" || got[0].Sha != "o1" {
		t.Fatalf("got %+v", got)
	}
}
