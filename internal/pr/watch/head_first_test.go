package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Under load, one PR's artifact requests held the poll loop. Each other PR's
// time limit ran out while its finished snapshot waited, so its new head was
// never stored and PR 404 never got a review round.
func TestSlowArtifactsDoNotBlockHeadRefreshOrReview(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase name="fast" time="1"/></testsuite>`})
	e.write()
	r := e.track()
	keys := []string{r.Key}
	if err := e.s.Store.update(func(st *State) error {
		other := *st.Records[r.Key]
		other.Number++
		other.Key = fmt.Sprintf("github.com/owner/repo#%d", other.Number)
		other.ChangeID = other.Key
		st.Records[other.Key] = &other
		keys = append(keys, other.Key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	// Artifact reads run after the head refresh and review dispatch. The read
	// checks that order when it starts, then fails as a slow read would.
	newHead := strings.Repeat("b", 40)
	var reads int
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if !strings.Contains(strings.Join(cmd.Args, " "), "/artifacts") {
			return base(ctx, cmd)
		}
		reads++
		st, err := e.s.Store.Read()
		if err != nil {
			t.Error(err)
		}
		for _, key := range keys {
			got := st.Records[key]
			if got.Head != newHead {
				t.Errorf("%s: artifacts read before the head refresh: %s (error %q)", key, got.Head, got.Error)
			}
			if got.Reviewer == "" || got.RoundHead != got.Head {
				t.Errorf("%s: artifacts read before review dispatch: reviewer=%q roundHead=%q", key, got.Reviewer, got.RoundHead)
			}
		}
		return context.DeadlineExceeded
	}
	e.f.Pull.Head.SHA = newHead
	e.tick()
	if reads == 0 {
		t.Fatal("the tick read no artifacts")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if got := st.Records[key]; got.Head != newHead || got.Reviewer == "" {
			t.Fatalf("%s: a failed artifact read undid the refresh: head=%s reviewer=%q", key, got.Head, got.Reviewer)
		}
	}
}

// Budget checks now run after the head refresh. A green attempt that is over
// the test budget must still extend the failure streak, so three of them in a
// row pause CI wakes like any three failures.
func TestOverBudgetAttemptsKeepTheFailureStreak(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase classname="pkg" name="slow" time="12.5"/></testsuite>`})
	e.track()
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			started := e.clock.Add(time.Minute)
			e.f.Runs[0].Attempt = attempt
			e.f.Runs[0].Started = started.Format(time.RFC3339)
			e.f.Pull.Updated = started.Format(time.RFC3339)
			listing := "repos/owner/repo/actions/runs/81/artifacts?per_page=100"
			e.f.Raw[listing] = strings.Replace(e.f.Raw[listing], `"created_at":"`, `"created_at":"`+started.Add(time.Second).Format(time.RFC3339)+`","old":"`, 1)
		}
		e.tick()
		r := e.record()
		if r.CI != "failed" || r.Failures != attempt {
			t.Fatalf("attempt %d: CI=%s Failures=%d", attempt, r.CI, r.Failures)
		}
	}
	if r := e.record(); !r.CIPaused {
		t.Fatalf("three over-budget attempts did not pause CI wakes: %+v", r)
	}
}
