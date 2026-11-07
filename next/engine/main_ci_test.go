package engine

import (
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/engine/store"
	"slices"
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
	return r.after(t, time.Minute)
}

// after moves the clock on, passes, and returns the GraphQL queries the pass made.
func (r *prRig) after(t *testing.T, d time.Duration) int {
	t.Helper()
	r.clock.add(d)
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

var (
	checksPassed  = fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS", URL: "https://x/build"}}}
	checksRunning = fakegithub.Commit{Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "build"}}}
)

// moveMain makes sha the head of owner/repo, with the CI GitHub shows for it.
func (r *prRig) moveMain(repo contract.RepoName, sha string, commit fakegithub.Commit) {
	r.github.SetMain(repo, sha)
	r.github.SetCommit(repo, sha, commit)
}

// toldShas lists the sha of each git.main_ci_finished so far, oldest first.
func (r *prRig) toldShas(t *testing.T) []string {
	t.Helper()
	var shas []string
	for _, event := range r.mainCIs(t) {
		shas = append(shas, event.Sha)
	}
	return shas
}

func requireShas(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("told shas %v, want %v", got, want)
	}
}

// The sequence of a busy repository: each head is replaced by the next before its CI is done.
func TestMainCIFinishedFiresForAHeadAfterTheBranchMovedOn(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", checksRunning)
	r.after(t, 0)
	r.moveMain("owner/repo", "B", checksRunning)
	r.after(t, 6*time.Minute)
	requireShas(t, r.toldShas(t))
	r.moveMain("owner/repo", "C", checksRunning)
	r.github.SetCommit("owner/repo", "A", checksPassed)
	r.after(t, 6*time.Minute)
	requireShas(t, r.toldShas(t), "A")
	r.next(t)
	requireShas(t, r.toldShas(t), "A")
	r.connect()
	r.next(t)
	requireShas(t, r.toldShas(t), "A")
	r.github.SetCommit("owner/repo", "B", checksPassed)
	r.next(t)
	requireShas(t, r.toldShas(t), "A", "B")
	r.connect()
	r.next(t)
	r.github.SetCommit("owner/repo", "C", checksPassed)
	r.next(t)
	r.connect()
	r.next(t)
	requireShas(t, r.toldShas(t), "A", "B", "C")
}

func TestMainCIFinishedKeepsTheOrderTheHeadsWereSeenIn(t *testing.T) {
	r := mainRig(t)
	for _, sha := range []string{"A", "B", "C"} {
		r.moveMain("owner/repo", sha, checksRunning)
		r.next(t)
	}
	r.github.SetCommit("owner/repo", "C", checksPassed)
	r.github.SetCommit("owner/repo", "A", checksPassed)
	r.github.SetCommit("owner/repo", "B", checksPassed)
	r.next(t)
	requireShas(t, r.toldShas(t), "A", "B", "C")
}

func TestMainCIFinishedFailureOfAHeadTheBranchMovedOnFrom(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", checksRunning)
	r.next(t)
	r.moveMain("owner/repo", "B", checksRunning)
	r.github.SetCommit("owner/repo", "A", fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "test", Conclusion: "FAILURE", URL: "https://x/test"}}})
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Sha != "A" || got[0].Result != contract.PrCIFailure || len(got[0].Runs) != 1 || got[0].Runs[0].Conclusion != "FAILURE" {
		t.Fatalf("got %+v", got)
	}
}

func TestMainCIFinishedHeadWithNoChecksSettlesTenMinutesAfterItsOwnFirstRead(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", fakegithub.Commit{})
	r.after(t, 0)
	r.moveMain("owner/repo", "B", fakegithub.Commit{})
	r.after(t, 6*time.Minute)
	r.connect()
	r.after(t, 3*time.Minute)
	if sizes := r.github.HeadQuerySizes(); sizes[len(sizes)-1] != 2 {
		t.Fatalf("both heads are still inside the grace, queried %v", sizes)
	}
	r.after(t, time.Minute)
	r.after(t, time.Minute)
	if sizes := r.github.HeadQuerySizes(); sizes[len(sizes)-1] != 1 {
		t.Fatalf("A settled at 10 minutes of its own age and B is 5, so only B is read, queried %v", sizes)
	}
	r.after(t, 5*time.Minute)
	r.github.SetCommit("owner/repo", "A", checksPassed)
	if queries := r.after(t, time.Minute); queries != 0 {
		t.Fatalf("both heads are settled and cost %d queries", queries)
	}
	requireShas(t, r.toldShas(t))
}

func TestMainCIFinishedDropsAHeadPastTheListWithNoEvent(t *testing.T) {
	r := mainRig(t)
	var heads []string
	for i := 0; i < 21; i++ {
		heads = append(heads, fmt.Sprintf("h%02d", i))
		r.moveMain("owner/repo", heads[i], checksRunning)
		r.next(t)
	}
	if sizes := r.github.HeadQuerySizes(); sizes[len(sizes)-1] != 20 {
		t.Fatalf("the list holds the newest 20 heads, the last query asked %d", sizes[len(sizes)-1])
	}
	for _, sha := range heads {
		r.github.SetCommit("owner/repo", sha, checksPassed)
	}
	r.next(t)
	r.connect()
	r.next(t)
	requireShas(t, r.toldShas(t), heads[1:]...)
}

func TestMainCIFinishedDropsAHeadPastTwoHoursWithNoEvent(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", checksRunning)
	r.next(t)
	if queries := r.after(t, 2*time.Hour); queries != 0 {
		t.Fatalf("a head past two hours cost %d queries", queries)
	}
	r.moveMain("owner/repo", "B", checksRunning)
	r.next(t)
	r.github.SetCommit("owner/repo", "A", checksPassed)
	r.next(t)
	r.connect()
	r.next(t)
	requireShas(t, r.toldShas(t))
}

// Passing a sha the list does not hold, or one already settled, writes nothing: this is the store's own guard.
func TestMainCIFinishedStoreSettlesAListedHeadOnce(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", checksRunning)
	r.next(t)
	settle := func(sha string) {
		pass := store.GitHubPass{MainCIs: []store.MainCI{{Repo: "owner/repo", Branch: "main", Head: sha, Result: contract.PrCISuccess}}}
		if err := r.s.store.ApplyPRPoll(r.ctx, pass, r.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	settle("never-listed")
	requireShas(t, r.toldShas(t))
	settle("A")
	settle("A")
	requireShas(t, r.toldShas(t), "A")
}

func TestMainCIFinishedDoesNotReportAHeadFromBeforeTheList(t *testing.T) {
	r := mainRig(t)
	r.github.SetCommit("owner/repo", "base", checksPassed)
	for i := 0; i < 3; i++ {
		if queries := r.next(t); queries != 0 {
			t.Fatalf("the stored head cost %d queries", queries)
		}
	}
	requireShas(t, r.toldShas(t))
}

func TestMainCIFinishedPollsOnlyTheUnsettledHeadsInOneQueryForARepository(t *testing.T) {
	r := newPRRig(t)
	if err := r.s.store.AddRoomRepo(r.ctx, r.room, "owner/other", false); err != nil {
		t.Fatal(err)
	}
	r.pass(t)
	for _, repo := range []contract.RepoName{"owner/repo", "owner/other"} {
		r.moveMain(repo, "A", checksRunning)
	}
	r.next(t)
	for _, repo := range []contract.RepoName{"owner/repo", "owner/other"} {
		r.moveMain(repo, "B", checksRunning)
	}
	before := len(r.github.HeadQuerySizes())
	if queries := r.next(t); queries != 2 {
		t.Fatalf("two repositories made %d queries, want one each", queries)
	}
	if sizes := r.github.HeadQuerySizes()[before:]; !slices.Equal(sizes, []int{2, 2}) {
		t.Fatalf("each query asks its repository's unsettled heads, got %v", sizes)
	}
	r.github.SetCommit("owner/repo", "A", checksPassed)
	r.github.SetCommit("owner/other", "A", checksPassed)
	r.next(t)
	before = len(r.github.HeadQuerySizes())
	r.next(t)
	if sizes := r.github.HeadQuerySizes()[before:]; !slices.Equal(sizes, []int{1, 1}) {
		t.Fatalf("settled heads are not asked again, got %v", sizes)
	}
	r.github.SetCommit("owner/repo", "B", checksPassed)
	r.github.SetCommit("owner/other", "B", checksPassed)
	r.next(t)
	if queries := r.next(t); queries != 0 {
		t.Fatalf("every head settled and a pass cost %d queries", queries)
	}
}

func TestMainCIFinishedDoesNotSettleUnreadContexts(t *testing.T) {
	r := mainRig(t)
	var checks []fakegithub.Check
	for i := 0; i < 150; i++ {
		checks = append(checks, fakegithub.Check{Name: fmt.Sprintf("check-%d", i), Conclusion: "SUCCESS"})
	}
	r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: "SUCCESS", Checks: checks})
	r.next(t)
	requireShas(t, r.toldShas(t))
	r.moveMain("owner/repo", "B", checksPassed)
	r.next(t)
	requireShas(t, r.toldShas(t), "B")
}

func TestMainCIFinishedNeverSettlesOnARollupThatIsNotTerminal(t *testing.T) {
	for name, commit := range map[string]fakegithub.Commit{
		"pending with one check done": {Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "lint", Conclusion: "SUCCESS"}, {Name: "test"}}},
		"expected status":             {Rollup: "EXPECTED", Checks: []fakegithub.Check{{Name: "lint", Conclusion: "SUCCESS"}, {Name: "required", Conclusion: "EXPECTED", StatusContext: true}}},
		"pending, queued only":        {Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "test"}}},
	} {
		t.Run(name, func(t *testing.T) {
			r := mainRig(t)
			r.moveMain("owner/repo", "A", commit)
			for i := 0; i < 15; i++ {
				if queries := r.next(t); queries != 1 {
					t.Fatalf("pass %d made %d queries, want 1", i, queries)
				}
			}
			requireShas(t, r.toldShas(t))
		})
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

// f319701's mix: a cancelled check turns the rollup FAILURE while other checks still run.
var (
	mixRunning   = fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "verify"}, {Name: "build linux", Status: "QUEUED"}, {Name: "build macos", Status: "WAITING"}, {Name: "build macos-x64", Status: "COMPLETED", Conclusion: "CANCELLED"}, {Name: "lint", Conclusion: "SUCCESS"}}}
	mixFinished  = fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "verify", Conclusion: "SUCCESS"}, {Name: "build linux", Conclusion: "SUCCESS"}, {Name: "build macos", Conclusion: "SKIPPED"}, {Name: "build macos-x64", Conclusion: "CANCELLED"}, {Name: "lint", Conclusion: "NEUTRAL"}}}
	mixFailedRun = fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "verify", Conclusion: "FAILURE"}, {Name: "build linux", Conclusion: "SUCCESS"}, {Name: "build macos", Conclusion: "SUCCESS"}, {Name: "build macos-x64", Conclusion: "CANCELLED"}, {Name: "lint", Conclusion: "SUCCESS"}}}
)

func TestMainCIFinishedWaitsForEveryContextAndReportsCancellation(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "f319701", mixRunning)
	for i := 0; i < 3; i++ {
		r.next(t)
	}
	requireShas(t, r.toldShas(t))
	r.github.SetCommit("owner/repo", "f319701", mixFinished)
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Result != contract.PrCICancelled {
		t.Fatalf("every check done and cancellation reported: %+v", got)
	}
	if !slices.Contains(got[0].Runs, contract.CIRun{Name: "build macos-x64", Conclusion: "CANCELLED"}) {
		t.Fatalf("the cancelled run is not listed: %+v", got[0].Runs)
	}
	r.next(t)
	requireShas(t, r.toldShas(t), "f319701")
}

func TestMainCIFinishedFailureWaitsForTheOtherContextsToFinish(t *testing.T) {
	r := mainRig(t)
	running := fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "verify", Conclusion: "FAILURE"}, {Name: "build linux", Status: "IN_PROGRESS"}, {Name: "build macos-x64", Conclusion: "CANCELLED"}}}
	r.moveMain("owner/repo", "A", running)
	for i := 0; i < 3; i++ {
		r.next(t)
	}
	requireShas(t, r.toldShas(t))
	r.github.SetCommit("owner/repo", "A", mixFailedRun)
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Result != contract.PrCIFailure {
		t.Fatalf("a failed run once all finished: %+v", got)
	}
}

func TestMainCIFinishedWithEveryContextCancelledIsCancelled(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "build", Conclusion: "CANCELLED"}, {Name: "test", Conclusion: "CANCELLED"}}})
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Result != contract.PrCICancelled || len(got[0].Runs) != 2 || got[0].Runs[0].Conclusion != "CANCELLED" {
		t.Fatalf("nothing ran and passed: %+v", got)
	}
}

func TestMainCIFinishedEveryFailingConclusionIsAFailure(t *testing.T) {
	for _, conclusion := range []string{"FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED", "STALE"} {
		t.Run(conclusion, func(t *testing.T) {
			r := mainRig(t)
			r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}, {Name: "test", Conclusion: conclusion}}})
			r.next(t)
			if got := r.mainCIs(t); len(got) != 1 || got[0].Result != contract.PrCIFailure {
				t.Fatalf("got %+v", got)
			}
		})
	}
	for _, state := range []string{"FAILURE", "ERROR"} {
		t.Run("status "+state, func(t *testing.T) {
			r := mainRig(t)
			r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: "FAILURE", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}, {Name: "ci/external", Conclusion: state, StatusContext: true}}})
			r.next(t)
			if got := r.mainCIs(t); len(got) != 1 || got[0].Result != contract.PrCIFailure {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestMainCIFinishedPendingStatusContextHoldsTheHead(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}, {Name: "ci/external", Conclusion: "PENDING", StatusContext: true}}})
	for i := 0; i < 3; i++ {
		r.next(t)
	}
	requireShas(t, r.toldShas(t))
	r.github.SetCommit("owner/repo", "A", fakegithub.Commit{Rollup: "SUCCESS", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}, {Name: "ci/external", Conclusion: "SUCCESS", StatusContext: true}}})
	r.next(t)
	got := r.mainCIs(t)
	if len(got) != 1 || got[0].Result != contract.PrCISuccess || !slices.Contains(got[0].Runs, contract.CIRun{Name: "ci/external", Conclusion: "SUCCESS"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestMainCIFinishedDecidesFromTheContextsAndNotTheRollupState(t *testing.T) {
	r := mainRig(t)
	r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: "PENDING", Checks: []fakegithub.Check{{Name: "build", Conclusion: "SUCCESS"}, {Name: "test", Conclusion: "SUCCESS"}}})
	r.next(t)
	if got := r.mainCIs(t); len(got) != 1 || got[0].Result != contract.PrCISuccess {
		t.Fatalf("every context completed under a rollup not yet updated: %+v", got)
	}
}

func TestMainCIFinishedTruncatedReadStaysPendingRegardlessOfRollup(t *testing.T) {
	var checks []fakegithub.Check
	for i := 0; i < 150; i++ {
		checks = append(checks, fakegithub.Check{Name: fmt.Sprintf("check-%d", i), Conclusion: "SUCCESS"})
	}
	for _, rollup := range []string{"SUCCESS", "FAILURE", "PENDING", "ERROR"} {
		t.Run(rollup, func(t *testing.T) {
			r := mainRig(t)
			r.moveMain("owner/repo", "A", fakegithub.Commit{Rollup: rollup, Checks: checks})
			for i := 0; i < 15; i++ {
				r.next(t)
			}
			requireShas(t, r.toldShas(t))
		})
	}
}
