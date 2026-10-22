package cihealth

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{DataDir: t.TempDir()}
}

func at(minutesAgo int) time.Time {
	return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Add(-time.Duration(minutesAgo) * time.Minute)
}

func testRun(room, token string, started time.Time) Test {
	return Test{Room: room, Test: "internal/pr/watch/TestSlow", Package: "internal/pr/watch", Token: token,
		PR: 1902, Head: "abc123", Started: started, Duration: 31 * time.Second, Limit: 10 * time.Second}
}

func TestSaveRunKeepsJobsAndSteps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	run := Run{Room: "r1", Token: "1241/1", Repo: "dyna-agent", PR: 1902, Head: "abc123", Workflow: "CI",
		Started: at(30), Status: "completed", Conclusion: "success", Queue: 12 * time.Second, Busy: 96 * time.Second,
		Warn: 2 * time.Minute, Fail: 3 * time.Minute}
	jobs := []Job{
		{ID: 7, Name: "go-test", Status: "completed", Conclusion: "success", Started: at(29), Completed: at(28),
			Busy: 70 * time.Second, Steps: []Step{{Name: "Set up job", Duration: 3 * time.Second}, {Name: "Run tests", Duration: 67 * time.Second}}},
		{ID: 8, Name: "frontend", Status: "completed", Conclusion: "failure", Started: at(29), Completed: at(28), Busy: 26 * time.Second},
	}
	if err := s.SaveRun(ctx, run, jobs); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, "r1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if got := runs[0]; got.Busy != 96*time.Second || got.Queue != 12*time.Second || got.Warn != 2*time.Minute || got.Fail != 3*time.Minute {
		t.Fatalf("run times: %+v", got)
	}
	detail, err := s.Run(ctx, "r1", "1241/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Jobs) != 2 || detail.Jobs[0].Name != "go-test" || detail.Jobs[0].Busy != 70*time.Second {
		t.Fatalf("jobs: %+v", detail.Jobs)
	}
	if steps := detail.Jobs[0].Steps; len(steps) != 2 || steps[1].Name != "Run tests" || steps[1].Duration != 67*time.Second {
		t.Fatalf("steps: %+v", steps)
	}
	// A re-inspected run replaces its jobs instead of adding a second copy.
	if err := s.SaveRun(ctx, run, jobs[1:]); err != nil {
		t.Fatal(err)
	}
	if detail, err = s.Run(ctx, "r1", "1241/1"); err != nil || len(detail.Jobs) != 1 {
		t.Fatalf("jobs after rewrite: %+v %v", detail.Jobs, err)
	}
}

// The graph reads many runs' jobs in one query, keyed by run token.
func TestJobsOfSeveralRuns(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i, token := range []string{"1241/1", "1250/1"} {
		run := Run{Room: "r1", Token: token, Started: at(i + 1)}
		jobs := []Job{{ID: int64(10 * (i + 1)), Name: "go-test", Busy: time.Duration(60+i) * time.Second},
			{ID: int64(10*(i+1) + 1), Name: "frontend", Busy: 20 * time.Second, Steps: []Step{{Name: "npm ci", Duration: 15 * time.Second}}}}
		if err := s.SaveRun(ctx, run, jobs); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.Jobs(ctx, "r1", []string{"1241/1", "1250/1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs["1241/1"]) != 2 || len(jobs["1250/1"]) != 2 {
		t.Fatalf("jobs per run: %+v", jobs)
	}
	if first := jobs["1241/1"][1]; first.Name != "frontend" || first.Busy != 20*time.Second || len(first.Steps) != 1 {
		t.Fatalf("frontend job: %+v", first)
	}
}

func TestSaveTestsKeepsTheLongestTimeInARun(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := testRun("r1", "1241/1", at(30))
	if err := s.SaveTests(ctx, []Test{first}); err != nil {
		t.Fatal(err)
	}
	shorter := first
	shorter.Duration = 12 * time.Second
	if err := s.SaveTests(ctx, []Test{shorter}); err != nil {
		t.Fatal(err)
	}
	// A run's own row is written by the budget pass; the test history stands alone.
	histories, err := s.Tests(ctx, "r1")
	if err != nil || len(histories) != 1 || len(histories[0].Runs) != 1 {
		t.Fatalf("history: %+v %v", histories, err)
	}
	if got := histories[0].Runs[0]; got.Duration != 31*time.Second || got.Limit != 10*time.Second {
		t.Fatalf("a retried test shrank: %+v", got)
	}
}

func TestCrossingsNeedTwoRunsAnHourApart(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := testRun("r1", "1241/1", at(180))
	if err := s.SaveTests(ctx, []Test{first}); err != nil {
		t.Fatal(err)
	}
	// This run breached half an hour after the first: one loaded-host episode.
	closeIn := testRun("r1", "1250/1", at(150))
	if err := s.SaveTests(ctx, []Test{closeIn}); err != nil {
		t.Fatal(err)
	}
	crossings, err := s.Crossings(ctx, "r1", "1250/1", []string{first.Test}, time.Hour)
	if err != nil || len(crossings) != 0 {
		t.Fatalf("one episode crossed: %+v %v", crossings, err)
	}
	// A run two hours later is a separate episode, and the nearest one wins.
	later := testRun("r1", "1277/1", at(10))
	later.Duration = 34 * time.Second
	if err := s.SaveTests(ctx, []Test{later}); err != nil {
		t.Fatal(err)
	}
	crossings, err = s.Crossings(ctx, "r1", "1277/1", []string{first.Test}, time.Hour)
	if err != nil || len(crossings) != 1 {
		t.Fatalf("crossings: %+v %v", crossings, err)
	}
	if crossings[0].Now.Duration != 34*time.Second || crossings[0].Other.Token != "1250/1" {
		t.Fatalf("crossing times: %+v", crossings[0])
	}
	// One line per test: a reported test stays out of later ticks.
	if err := s.MarkNotified(ctx, "r1", []string{first.Test}, "1277/1"); err != nil {
		t.Fatal(err)
	}
	crossings, err = s.Crossings(ctx, "r1", "1277/1", []string{first.Test}, time.Hour)
	if err != nil || len(crossings) != 0 {
		t.Fatalf("reported test crossed twice: %+v %v", crossings, err)
	}
}

func TestTestHistoryIsNewestFirstAndCapped(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := range KeepTests + 5 {
		row := testRun("r1", "run-"+strconv.Itoa(i), at(i))
		row.Duration = time.Duration(11+i) * time.Second
		if err := s.SaveTests(ctx, []Test{row}); err != nil {
			t.Fatal(err)
		}
	}
	histories, err := s.Tests(ctx, "r1")
	if err != nil || len(histories) != 1 {
		t.Fatalf("histories: %+v %v", histories, err)
	}
	runs := histories[0].Runs
	if len(runs) != KeepTests {
		t.Fatalf("history length: %d", len(runs))
	}
	if runs[0].Started.Before(runs[1].Started) {
		t.Fatalf("history is not newest first: %+v", runs[:2])
	}
}

func TestPruneKeepsTheNewestRunsAndReopensQuietTests(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := range KeepRuns + 3 {
		run := Run{Room: "r1", Token: "run-" + strconv.Itoa(i), Started: at(i), Busy: time.Duration(i) * time.Second}
		if err := s.SaveRun(ctx, run, []Job{{ID: int64(i), Name: "go-test", Busy: time.Second}}); err != nil {
			t.Fatal(err)
		}
	}
	long := testRun("r1", "old-run", at(60*24*10))
	if err := s.SaveTests(ctx, []Test{long}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNotified(ctx, "r1", []string{long.Test}, "old-run"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx, at(0)); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, "r1", KeepRuns+10)
	if err != nil || len(runs) != KeepRuns {
		t.Fatalf("runs after prune: %d %v", len(runs), err)
	}
	detail, err := s.Run(ctx, "r1", runs[0].Token)
	if err != nil || len(detail.Jobs) != 1 {
		t.Fatalf("jobs after prune: %+v %v", detail.Jobs, err)
	}
	// The test breached ten days ago and not since, so a new breach reports.
	notified, err := s.Notified(ctx, "r1", []string{long.Test})
	if err != nil || notified[long.Test] {
		t.Fatalf("quiet test stayed notified: %+v %v", notified, err)
	}
	if histories, err := s.Tests(ctx, "r1"); err != nil || len(histories) != 1 {
		t.Fatalf("quiet history dropped: %+v %v", histories, err)
	}
}
