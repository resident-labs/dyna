package watch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
)

const scopeReport = `<testsuite>
<testcase classname="dyna-agent/internal/x" name="TestChangedSlow" time="2"/>
<testcase classname="dyna-agent/internal/y" name="TestOtherSlow" time="3"/>
<testcase classname="test" name="the sidebar renders" time="2"/>
<testcase classname="dyna-agent/internal/x" name="TestFast" time="0.1"/>
</testsuite>`

func prFilesFixture(e *testEnv, number int, files ...prFile) {
	data, _ := json.Marshal(files)
	e.f.Raw[fmt.Sprintf("repos/owner/repo/pulls/%d/files?per_page=100", number)] = "HTTP/2.0 200 OK\r\n\r\n" + string(data)
}

func scopeEnv(t *testing.T, files ...prFile) *testEnv {
	t.Helper()
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": scopeReport})
	prFilesFixture(e, 17, files...)
	p := RoomPolicy(State{}, e.author.RoomID)
	p.CITimeBudget = "enforce"
	p.CITestMax = "1s"
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTestBudgetFailsOnlyTestsThePRChanges(t *testing.T) {
	e := scopeEnv(t, prFile{Name: "internal/x/x_test.go", Patch: "@@ -1,3 +1,6 @@\n+func TestChangedSlow(t *testing.T) {\n+}\n"})
	e.track()
	e.tick()
	r := e.record()
	if r.CI != "failed" || !strings.HasPrefix(r.CIReason, "tests over 1s: dyna-agent/internal/x/TestChangedSlow; warning, outside this PR: ") {
		t.Fatalf("ci=%s reason=%q", r.CI, r.CIReason)
	}
	if run := r.CITestRuns["81/1"]; !strings.Contains(run, "TestOtherSlow") || !strings.Contains(run, "the sidebar renders") {
		t.Fatalf("other slow tests are not reported as warnings: %q", run)
	}
}

func TestTestBudgetWarnsForSlowTestsOutsideThePR(t *testing.T) {
	// The PR changes a test file, but not the slow tests in it.
	e := scopeEnv(t, prFile{Name: "internal/x/x_test.go", Patch: "@@ -12,1 +12,1 @@ func TestFast(t *testing.T) {\n-\t_ = 0\n+\t_ = 1\n"})
	contentFixture(e, "internal/x/x_test.go", spanGoFile)
	e.track()
	e.tick()
	r := e.record()
	if r.CI == "failed" || !strings.HasPrefix(r.CITestRuns["81/1"], "warning: tests over 1s outside this PR: ") {
		t.Fatalf("ci=%s runs=%v", r.CI, r.CITestRuns)
	}
	for _, n := range e.sent {
		if strings.Contains(n.Text, "TestOtherSlow") {
			t.Fatalf("a warning woke the author: %+v", n)
		}
	}
}

func TestTestBudgetMatchesChangedWebTestsByName(t *testing.T) {
	e := scopeEnv(t, prFile{Name: "web/src/lib/Sidebar.test.js", Patch: "@@ -1 +1,3 @@\n+test('the sidebar renders', () => {\n+})\n"})
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" || !strings.HasPrefix(r.CIReason, "tests over 1s: test/the sidebar renders;") {
		t.Fatalf("ci=%s reason=%q", r.CI, r.CIReason)
	}
}

func TestTestBudgetRechecksVerdictsFromTheUnscopedLimit(t *testing.T) {
	e := scopeEnv(t)
	r := e.track()
	old, _ := json.Marshal(struct {
		Max   string
		Allow []string
		Warn  bool
		Mode  string
	}{"1s", nil, true, "enforce"})
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.CITestHead, live.CITestPolicy = e.f.Pull.Head.SHA, digest(string(old))
		live.CITestRuns = map[string]string{"81/1": "tests over 1s: dyna-agent/internal/y/TestOtherSlow"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.record(); got.CI == "failed" || !strings.HasPrefix(got.CITestRuns["81/1"], "warning: ") {
		t.Fatalf("old verdict kept: ci=%s runs=%v", got.CI, got.CITestRuns)
	}
}

// GitHub lists a PR's files against its current base. A retarget changes that
// list without a new head or CI run, so the saved verdict is checked again.
func TestTestBudgetRechecksVerdictAfterRetarget(t *testing.T) {
	e := scopeEnv(t, prFile{Name: "internal/x/x_test.go"})
	e.f.Pull.Base.Ref = "feature-base"
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" {
		t.Fatalf("setup: the old base's files should fail the PR: ci=%s runs=%v", r.CI, r.CITestRuns)
	}
	e.f.Pull.Base.Ref = "main"
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	prFilesFixture(e, 17, prFile{Name: "web/other.test.js", Patch: "@@ -1 +1 @@\n-x\n+y\n"})
	contentFixture(e, "web/other.test.js", "y\n")
	e.tick()
	if r := e.record(); r.CI == "failed" || !strings.HasPrefix(r.CITestRuns["81/1"], "warning: ") {
		t.Fatalf("verdict from the old base was kept after the retarget: ci=%s runs=%v", r.CI, r.CITestRuns)
	}
}

// One loaded-host episode breaches many PRs' runs within minutes and flags
// whatever happens to be running. Root decision on the four issues filed at
// 12:18: file a slow-test issue only when the same test breaches in two
// separate CI runs. A saved warning that cannot show a separate run — no
// recorded run time, like every state written before this policy — must not
// file an issue.
func TestLargeDiffFileBlamesOnlyTestsItHolds(t *testing.T) {
	t.Run("other file", func(t *testing.T) {
		e := scopeEnv(t, prFile{Name: "web/src/lib/settings/profileUse.test.js"})
		contentFixture(e, "web/src/lib/settings/profileUse.test.js", "test('profile saves', () => {})\n")
		e.track()
		e.tick()
		r := e.record()
		if run := r.CITestRuns["81/1"]; r.CI == "failed" || !strings.HasPrefix(run, "warning: tests over 1s outside this PR: ") {
			t.Fatalf("ci=%s runs=%v, want every slow test outside the PR", r.CI, r.CITestRuns)
		}
	})
	t.Run("own test", func(t *testing.T) {
		e := scopeEnv(t, prFile{Name: "web/src/Sidebar.test.js"})
		contentFixture(e, "web/src/Sidebar.test.js", spanJSFile)
		e.track()
		e.tick()
		r := e.record()
		if !strings.HasPrefix(r.CIReason, "tests over 1s: test/the sidebar renders") {
			t.Fatalf("ci=%s reason=%q, want the test this file holds", r.CI, r.CIReason)
		}
	})
}
