package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func checkDetailsJSON(t *testing.T, r Record) string {
	t.Helper()
	b, err := json.Marshal(r.Checks)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFailedCheckSavesJUnitExcerptOnce(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase name="broken"><failure message="expected green">got red</failure></testcase></testsuite>`})
	e.f.Runs[0].Conclusion = "failure"
	e.f.Checks = []Check{{ID: 91, Name: "unit", Status: "completed", Conclusion: "failure", URL: "https://github.com/owner/repo/actions/runs/81/job/91"}}
	e.f.Checks[0].App.Slug = "github-actions"
	e.track()
	e.tick()
	got := checkDetailsJSON(t, e.record())
	if !strings.Contains(got, "FAIL broken") || !strings.Contains(got, "expected green") {
		t.Fatalf("missing check excerpt: %s", got)
	}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "actions/artifacts/91/zip") != 1 {
		t.Fatalf("repeated artifact request: %s", calls)
	}
	if !strings.Contains(checkDetailsJSON(t, e.record()), "FAIL broken") {
		t.Fatal("poll dropped saved excerpt")
	}
}

func TestFailedChecksSaveOwnJobLogTailAndCapRequests(t *testing.T) {
	e := newEnv(t)
	e.f.Checks = nil
	e.f.Raw = map[string]string{}
	for i := int64(1); i <= 6; i++ {
		name := fmt.Sprintf("job-%d", i)
		e.f.Checks = append(e.f.Checks, actionsCheck(Check{ID: i, Name: name, Status: "completed", Conclusion: "failure", URL: fmt.Sprintf("https://github.com/owner/repo/actions/runs/81/job/%d", i+100)}))
		var lines []string
		for n := 0; n < 45; n++ {
			lines = append(lines, fmt.Sprintf("%s line %d", name, n))
		}
		e.f.Raw[fmt.Sprintf("repos/owner/repo/actions/jobs/%d/logs", i+100)] = "HTTP/2.0 200 OK\r\n\r\n" + strings.Join(lines, "\n")
	}
	e.track()
	e.tick()
	r := e.record()
	for i, c := range r.Checks {
		if i < 5 {
			if !strings.Contains(c.FailureExcerpt, c.Name+" line 44") || strings.Contains(c.FailureExcerpt, c.Name+" line 14") {
				t.Fatalf("wrong log tail: %+v", c)
			}
		} else if c.FailureExcerpt != "" {
			t.Fatal("fetched over five failed checks")
		}
	}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "/logs") != 5 {
		t.Fatalf("log requests not bounded/cached: %s", calls)
	}
}

func TestJobLogExcerptKeepsFailureBeforeCleanup(t *testing.T) {
	e := newEnv(t)
	e.f.Checks = []Check{actionsCheck(Check{ID: 101, Name: "go-test", Status: "completed", Conclusion: "failure"})}
	lines := []string{"starting", "2026-10-19T08:58:47Z --- FAIL: TestBroken (0.01s)", "expected green, got red", strings.Repeat("diagnostic ", 1000)}
	for i := 0; i < 80; i++ {
		lines = append(lines, fmt.Sprintf("cleanup %d", i))
	}
	e.f.Raw = map[string]string{"repos/owner/repo/actions/jobs/101/logs": "HTTP/2.0 200 OK\r\n\r\n" + strings.Join(lines, "\n")}
	e.track()
	e.tick()
	got := e.record().Checks[0].FailureExcerpt
	if !strings.Contains(got, "--- FAIL: TestBroken") || !strings.Contains(got, "expected green") || strings.Contains(got, "cleanup 79") {
		t.Fatalf("failure hidden by cleanup: %s", got)
	}
}

func TestFailedJobsInOneRunKeepTheirOwnFailure(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase name="unit-only"><failure>unit assertion failed</failure></testcase></testsuite>`})
	e.f.Runs[0].Conclusion = "failure"
	e.f.Checks = []Check{
		actionsCheck(Check{ID: 101, Name: "unit", Status: "completed", Conclusion: "failure", URL: "https://github.com/owner/repo/actions/runs/81/job/101"}),
		actionsCheck(Check{ID: 102, Name: "lint", Status: "completed", Conclusion: "failure", URL: "https://github.com/owner/repo/actions/runs/81/job/102"}),
	}
	e.f.Raw["repos/owner/repo/actions/jobs/101/logs"] = "HTTP/2.0 200 OK\r\n\r\nunit assertion failed"
	e.f.Raw["repos/owner/repo/actions/jobs/102/logs"] = "HTTP/2.0 200 OK\r\n\r\nlint rejected unused import"
	e.track()
	e.tick()
	for _, c := range e.record().Checks {
		want := "unit assertion failed"
		if c.Name == "lint" {
			want = "lint rejected unused import"
		}
		if c.FailureExcerpt != want {
			t.Fatalf("%s got another job's failure: %q", c.Name, c.FailureExcerpt)
		}
	}
}

func TestGoJobLogExcerptShowsOnlyFailedTestBlocks(t *testing.T) {
	lines := []string{
		"=== RUN   TestParent",
		"=== RUN   TestParent/pass",
		"--- PASS: TestParent/pass (0.01s)",
		"=== RUN   TestParent/question",
		"    room_work_guards_test.go:57: lost protected work",
		"--- FAIL: TestParent/question (0.05s)",
		"=== RUN   TestParent/other",
		"--- PASS: TestParent/other (0.01s)",
		"--- FAIL: TestParent (0.13s)",
		"=== RUN   TestSecond",
		"    second_test.go:12: expected green",
		"--- FAIL: TestSecond (0.01s)",
		"=== RUN   TestUnrelated",
		"--- PASS: TestUnrelated (0.01s)",
		"cleanup",
	}
	for i := range lines {
		lines[i] = "2026-10-19T08:58:47.7415794Z " + lines[i]
	}
	got := failureExcerpt(strings.Join(lines, "\n"), true)
	want := "=== RUN   TestParent/question\n    room_work_guards_test.go:57: lost protected work\n--- FAIL: TestParent/question (0.05s)\n\n=== RUN   TestSecond\n    second_test.go:12: expected green\n--- FAIL: TestSecond (0.01s)"
	if got != want {
		t.Fatalf("go failure excerpt:\n%s\nwant:\n%s", got, want)
	}
}

func TestLongGoJobLogBlockKeepsDiagnosticAndFailure(t *testing.T) {
	for _, largeLine := range []bool{false, true} {
		t.Run(fmt.Sprint(largeLine), func(t *testing.T) {
			lines := []string{"=== RUN   TestParent"}
			for i := 0; i < 20; i++ {
				lines = append(lines, fmt.Sprintf("=== RUN   TestParent/pass%d", i), fmt.Sprintf("--- PASS: TestParent/pass%d (0.01s)", i))
			}
			if largeLine {
				lines = append(lines, strings.Repeat("old diagnostic ", 600))
			}
			lines = append(lines, "    parent_test.go:57: fatal diagnostic", "--- FAIL: TestParent (0.13s)", "cleanup")
			got := failureExcerpt(strings.Join(lines, "\n"), true)
			if !strings.Contains(got, "fatal diagnostic") || !strings.Contains(got, "--- FAIL: TestParent") {
				t.Fatalf("long block hid failure: %s", got)
			}
			if strings.Contains(got, "--- PASS:") {
				t.Fatalf("unrelated pass remains: %s", got)
			}
			if len(got) > 4096 || len(strings.Split(got, "\n")) > 30 {
				t.Fatalf("excerpt exceeds cap: %d bytes", len(got))
			}
		})
	}
}
