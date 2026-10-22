package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/cibudget"
	"dyna-agent/internal/cihealth"
	"dyna-agent/internal/room"
)

// One inspected run fills the CI health rows: every test over the room's
// limit, then the slowest test under it. Nothing is filed as an issue.
func TestCIHealthRecordsTheRunsSlowTests(t *testing.T) {
	e := scopeEnv(t)
	e.track()
	e.tick()
	ctx := context.Background()
	histories, err := e.s.ciHealth().Tests(ctx, e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, history := range histories {
		names = append(names, history.Test)
		if len(history.Runs) != 1 {
			t.Fatalf("history of %s: %+v", history.Test, history.Runs)
		}
		if row := history.Runs[0]; row.Token != "81/1" || row.Limit != time.Second || row.PR != 17 {
			t.Fatalf("test row: %+v", row)
		}
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{"TestChangedSlow", "TestOtherSlow", "the sidebar renders", "TestFast"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rows lack %q:\n%s", want, joined)
		}
	}
	issues, err := room.DefaultStore().Issues(e.author.RoomID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("a slow test filed an issue: %+v", issues)
	}
}

// The budget pass hands the same run to the CI health store, which keeps the
// jobs and their steps for the drill-down.
func TestCIHealthStoresARunWithItsJobs(t *testing.T) {
	e := scopeEnv(t)
	e.track()
	rec := e.record()
	run := Run{ID: 81, Attempt: 1, Name: "CI", Status: "completed", Conclusion: "success",
		Event: "pull_request", Started: e.clock.Format(time.RFC3339), HeadSHA: e.f.Pull.Head.SHA}
	jobs := []cihealth.Job{
		{ID: 7, Name: "go-test", Status: "completed", Conclusion: "success", Busy: 70 * time.Second,
			Steps: []cihealth.Step{{Name: "Set up job", Duration: 3 * time.Second}, {Name: "Run tests", Duration: 67 * time.Second}}},
		{ID: 8, Name: "frontend", Status: "queued"},
	}
	e.s.saveCIRun(context.Background(), rec, run, 3*time.Minute, 4*time.Minute, 96*time.Second, 12*time.Second, jobs)
	stored, err := e.s.ciHealth().Run(context.Background(), rec.RoomID, "81/1")
	if err != nil {
		t.Fatal(err)
	}
	if r := stored.Run; r.PR != 17 || r.Workflow != "CI" || r.Busy != 96*time.Second || r.Queue != 12*time.Second || r.Warn != 3*time.Minute || r.Fail != 4*time.Minute {
		t.Fatalf("run row: %+v", r)
	}
	if len(stored.Jobs) != 2 {
		t.Fatalf("jobs: %+v", stored.Jobs)
	}
	if first := stored.Jobs[0]; first.Name != "go-test" || len(first.Steps) != 2 || first.Steps[1].Duration != 67*time.Second {
		t.Fatalf("job steps: %+v", first)
	}
	if waiting := stored.Jobs[1]; waiting.Busy != 0 || waiting.Status != "queued" {
		t.Fatalf("queued job: %+v", waiting)
	}
}

// A test that broke the limit twice, an hour apart, posts exactly one line,
// once. The line lands in the feed and wakes nobody.
func TestSlowTestCrossingPostsOneQuietLine(t *testing.T) {
	e := scopeEnv(t)
	e.track()
	ctx := context.Background()
	older := cihealth.Test{Room: e.author.RoomID, Test: "dyna-agent/internal/y/TestOtherSlow", Package: "dyna-agent/internal/y",
		Token: "80/1", PR: 16, Head: "b", Started: e.clock.Add(-2 * time.Hour), Duration: 3 * time.Second, Limit: time.Second}
	if err := e.s.ciHealth().SaveTests(ctx, []cihealth.Test{older}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	rec, err := room.DefaultStore().Resolve(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := room.DefaultStore().Messages(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, msg := range msgs {
		if msg.Kind != "test-health" {
			continue
		}
		lines = append(lines, msg.Text)
		if room.WakesRoot(msg, rec.Name) {
			t.Fatalf("the crossing line woke root: %+v", msg)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("crossing lines = %d:\n%s", len(lines), strings.Join(lines, "\n---\n"))
	}
	if line := lines[0]; !strings.Contains(line, "TestOtherSlow") || !strings.Contains(line, "#81") || !strings.Contains(line, "#80") || !strings.Contains(line, "limit 1 s") {
		t.Fatalf("crossing line: %q", line)
	}
	// The same test does not report again on the next tick.
	e.tick()
	msgs, err = room.DefaultStore().Messages(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, msg := range msgs {
		if msg.Kind == "test-health" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("crossing posted %d times", count)
	}
}

// A report that ran isolated re-runs counts a breach only when a run alone
// confirmed it. A loaded runner reports inflated full-run times, and a run of
// a single test is what tells a slow test from a slow host.
func TestOnlyConfirmedBreachesAreRecorded(t *testing.T) {
	limit := time.Second
	tests := []cibudget.Test{
		{Package: "p", Name: "TestConfirmed", Duration: 4 * time.Second, IsolationRan: true, Isolated: true},
		{Package: "p", Name: "TestLoaded", Duration: 37 * time.Second, IsolationRan: true},
		{Package: "p", Name: "TestFast", Duration: 20 * time.Millisecond, IsolationRan: true},
		// A suite without isolation has nothing to wait for.
		{Package: "relay", Name: "slow", Duration: 12 * time.Second},
	}
	var names []string
	for _, test := range recordableTests(tests, limit) {
		names = append(names, test.Name)
	}
	// Over-limit rows come first, longest first, then the under-limit ones.
	if strings.Join(names, ",") != "slow,TestConfirmed,TestFast" {
		t.Fatalf("stored %v", names)
	}
}

// The artifact is the only thing the watcher gets, and several reports merge
// into one before it looks. Each test must keep its own suite's provenance
// through that merge, or a loaded suite's times return as breaches.
func TestJUnitProvenanceSurvivesMerging(t *testing.T) {
	const isolated = `<testsuite name="go test" isolation="ran">
  <testcase classname="p" name="TestConfirmed" time="5.76" isolated="true"/>
  <testcase classname="p" name="TestLoaded" time="37"/>
  <testcase classname="p" name="TestFast" time="0.02"/>
</testsuite>`
	const plain = `<testsuite name="relay">
  <testcase classname="relay" name="slow" time="12.5"/>
</testsuite>`
	var merged cibudget.Report
	for _, artifact := range []string{isolated, plain} {
		var one cibudget.Report
		if err := one.ReadJUnit(strings.NewReader(artifact)); err != nil {
			t.Fatal(err)
		}
		merged.Tests = append(merged.Tests, one.Tests...)
		merged.Failures = append(merged.Failures, one.Failures...)
	}
	var names []string
	for _, test := range recordableTests(merged.Tests, time.Second) {
		names = append(names, test.ID())
	}
	// relay/slow is over with no isolation to wait for; TestConfirmed stayed
	// over when it ran alone; TestLoaded was never confirmed and is dropped.
	if strings.Join(names, ",") != "relay/slow,p/TestConfirmed,p/TestFast" {
		t.Fatalf("merged artifact stored %v", names)
	}
}
