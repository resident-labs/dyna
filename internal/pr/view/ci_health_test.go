package view

import (
	"testing"
	"time"

	"dyna-agent/internal/cihealth"
)

func TestTestStateNamesWhatToDo(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	limit := time.Second
	row := func(runs ...cihealth.Test) CIHealthTest {
		test := CIHealthTest{Breaches: 0}
		for _, run := range runs {
			if run.Over() {
				test.Breaches++
			}
		}
		return test
	}
	breach := func(hoursAgo int, seconds int) cihealth.Test {
		return cihealth.Test{Started: now.Add(-time.Duration(hoursAgo) * time.Hour),
			Duration: time.Duration(seconds) * time.Second, Limit: limit}
	}
	for _, tc := range []struct {
		name    string
		runs    []cihealth.Test
		ignored bool
		want    string
	}{
		{"no breach", []cihealth.Test{breach(1, 0)}, false, "quiet"},
		{"one breach", []cihealth.Test{breach(1, 2)}, false, "new"},
		{"two breaches in one episode", []cihealth.Test{breach(1, 2), breach(1, 3)}, false, "new"},
		{"two breaches an hour apart", []cihealth.Test{breach(1, 2), breach(4, 3)}, false, "recurring"},
		{"a breach a week old", []cihealth.Test{breach(24*8, 2), breach(24*9, 3)}, false, "quiet"},
		{"ignored wins", []cihealth.Test{breach(1, 3), breach(4, 3)}, true, "ignored"},
	} {
		if got := testState(row(tc.runs...), tc.runs, tc.ignored, now); got != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIgnoredTestReadsTheAllowList(t *testing.T) {
	allowed := map[string]bool{"internal/pr/watch/TestSlow": true, "TestBare": true, "internal/pr/watch/TestParent/child": true, "child": true}
	notes := map[string]string{
		"internal/pr/watch/TestSlow":         "measured 12 s alone; keep until the parser rewrite",
		"internal/pr/watch/TestParent/child": "waits on the trial gate",
	}
	if ignored, reason := ignoredTest("internal/pr/watch/TestSlow", "internal/pr/watch", allowed, notes); !ignored || reason == "" {
		t.Fatalf("qualified allow entry: %v %q", ignored, reason)
	}
	// Bare entries cannot exempt packaged tests.
	if ignored, _ := ignoredTest("internal/other/TestBare", "internal/other", allowed, notes); ignored {
		t.Fatal("bare allow entry exempted packaged test")
	}
	if ignored, _ := ignoredTest("internal/pr/watch/TestFast", "internal/pr/watch", allowed, notes); ignored {
		t.Fatal("a test outside the allow list is ignored")
	}
	// A subtest keeps its parent in the bare name, so allowing one subtest
	// neither allows its sibling nor matches the bare last segment.
	if ignored, reason := ignoredTest("internal/pr/watch/TestParent/child", "internal/pr/watch", allowed, notes); !ignored || reason != notes["internal/pr/watch/TestParent/child"] {
		t.Fatalf("subtest allow entry: %v %q", ignored, reason)
	}
	if ignored, _ := ignoredTest("internal/pr/watch/TestParent/sibling", "internal/pr/watch", allowed, notes); ignored {
		t.Fatal("a sibling subtest is allowed by another subtest")
	}
	// A bare "child" in the list must not match every subtest whose last
	// segment is child; that is the rule the budget uses.
	if ignored, _ := ignoredTest("internal/pr/watch/TestOther/child", "internal/pr/watch", allowed, notes); ignored {
		t.Fatal("a bare subtest segment allowed an unrelated subtest")
	}
	if ignored, _ := ignoredTest("internal/other/TestParent/child", "internal/other", allowed, notes); ignored {
		t.Fatal("qualified subtest ID matched another package")
	}
	if ignored, _ := ignoredTest("TestBare", "", allowed, notes); !ignored {
		t.Fatal("package-less literal ID rejected")
	}
	if got := bareName("internal/pr/watch/TestParent/child", "internal/pr/watch"); got != "TestParent/child" {
		t.Fatalf("bareName of a subtest = %q", got)
	}
	if got := bareName("test/src/lib/prs/mergeQueueScreen.test.js", "test/src/lib/prs/mergeQueueScreen.test.js"); got != "test/src/lib/prs/mergeQueueScreen.test.js" {
		t.Fatalf("bareName without a package prefix = %q", got)
	}
}

func TestMedianAndName(t *testing.T) {
	if got := bareName("dyna-agent/internal/pr/watch/TestSlow", "dyna-agent/internal/pr/watch"); got != "TestSlow" {
		t.Fatalf("bareName = %q", got)
	}
	if got := medianMs(nil); got != 0 {
		t.Fatalf("median of no runs = %d", got)
	}
	runs := []cihealth.Test{{Duration: 3 * time.Second}, {Duration: 9 * time.Second}, {Duration: 6 * time.Second}}
	if got := medianMs(runs); got != 6000 {
		t.Fatalf("median = %d ms", got)
	}
}

// A column draws the jobs that took time, in the order they started.
func TestJobBandsSkipJobsThatNeverRan(t *testing.T) {
	jobs := []cihealth.Job{
		{ID: 7, Name: "go-test", Busy: 70 * time.Second},
		{ID: 8, Name: "frontend-e2e", Status: "queued"},
		{ID: 9, Name: "cross-build", Busy: 21 * time.Second},
	}
	bands := jobBands(jobs)
	if len(bands) != 2 {
		t.Fatalf("bands: %+v", bands)
	}
	if bands[0].Name != "go-test" || bands[0].BusyMs != 70000 || bands[1].Name != "cross-build" {
		t.Fatalf("bands: %+v", bands)
	}
}

// A run's response names each slow test, so a client can open one without a
// second lookup, and two tests never share an identity.
func TestRunDetailNamesEachTest(t *testing.T) {
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	detail := cihealth.RunDetail{
		Run: cihealth.Run{Room: "r1", Token: "1241/1", PR: 1902, Busy: 90 * time.Second},
		Tests: []cihealth.Test{
			{Room: "r1", Test: "internal/pr/watch/TestSlow", Package: "internal/pr/watch", Token: "1241/1", PR: 1902, Started: started, Duration: 31 * time.Second, Limit: time.Second},
			{Room: "r1", Test: "test/src/lib/prs/mergeQueueScreen.test.js", Package: "test/src/lib/prs/mergeQueueScreen.test.js", Token: "1241/1", PR: 1902, Started: started, Duration: 4 * time.Second, Limit: time.Second},
		},
	}
	out := ciHealthRunDetail(detail)
	if len(out.Tests) != 2 {
		t.Fatalf("tests in response: %+v", out.Tests)
	}
	first, second := out.Tests[0], out.Tests[1]
	if first.Test != "internal/pr/watch/TestSlow" || first.Package != "internal/pr/watch" || first.DurationMs != 31000 || !first.Over {
		t.Fatalf("first test: %+v", first)
	}
	if second.Test != "test/src/lib/prs/mergeQueueScreen.test.js" || second.Test == first.Test {
		t.Fatalf("second test: %+v", second)
	}
	if out.Run.Token != "1241/1" || out.Run.BusyMs != 90000 {
		t.Fatalf("run: %+v", out.Run)
	}
}
