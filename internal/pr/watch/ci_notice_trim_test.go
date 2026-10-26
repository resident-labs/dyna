package watch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
)

// cancelledCIRegistry reports local CI mode with no usable node, so the room
// keeps the hosted fallback while Dyna's earlier cancellation stays saved.
type cancelledCIRegistry struct{ hosted localci.HostedRuns }

func (r *cancelledCIRegistry) Nodes(context.Context) ([]localci.Node, error) { return nil, nil }
func (r *cancelledCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *cancelledCIRegistry) SaveResult(context.Context, localci.Result) error { return nil }
func (r *cancelledCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}
func (r *cancelledCIRegistry) LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error) {
	return r.hosted, nil
}
func (r *cancelledCIRegistry) SaveHostedRuns(_ context.Context, _, _ string, runs localci.HostedRuns) error {
	r.hosted = runs
	return nil
}

// cancelledAggregateFixture makes run 81 the go-test aggregate that failed
// only because Dyna cancelled a hosted shard for its own local CI.
func cancelledAggregateFixture(t *testing.T, e *testEnv) {
	t.Helper()
	repo, base := changeRepo(t)
	changeGit(t, repo, "remote", "add", "origin", "https://github.com/owner/repo")
	head := changeCommit(t, repo, "head\n")
	e.author.Dir = repo
	testReportFixture(t, e, nil)
	e.f.Pull.Base.SHA = base
	e.f.Pull.Head.SHA = head
	e.f.Runs[0] = Run{
		ID: 81, Attempt: 1, CheckSuiteID: 7, Path: ".github/workflows/ci.yml",
		Event: "pull_request", HeadSHA: head, HeadBranch: "feature",
		Name: "CI", Status: "completed", Conclusion: "failure",
		Started: e.clock.Format(time.RFC3339),
	}
	e.f.Checks = nil
	e.f.Pull.Head.Repo.FullName = "owner/repo"
	for i, job := range []string{"go-test-shard (0)", "go-static", "frontend", "frontend-check", "desktop", "go-test"} {
		check := actionsCheck(Check{ID: int64(i + 1), Name: job, Status: "completed", Conclusion: "success"})
		check.CheckSuite.ID = 7
		if job == "go-test-shard (0)" {
			check.Conclusion = "cancelled"
		}
		if job == "go-test" {
			check.Conclusion = "failure"
		}
		e.f.Checks = append(e.f.Checks, check)
	}
	e.write()
}

// A hosted run that Dyna cancelled for local CI never tested the head, so its
// artifacts and its failed-job log must not reach a notice.
func TestLocalCancelledHostedRunSkipsLogAndReportInspection(t *testing.T) {
	e := newEnv(t)
	cancelledAggregateFixture(t, e)
	tracked := e.track()
	if !headSHAPattern.MatchString(tracked.Base) {
		t.Fatalf("fixture did not track a verified base: %+v", tracked)
	}
	e.s.LocalCIRegistry = &cancelledCIRegistry{hosted: localci.HostedRuns{IDs: []int64{81}, Attempts: map[int64]int{81: 1}}}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	for _, unwanted := range []string{"/artifacts?", "--log-failed", "actions/jobs/"} {
		if strings.Contains(string(calls), unwanted) {
			t.Fatalf("inspection of a cancelled run reached %s:\n%s", unwanted, calls)
		}
	}
	for _, sent := range e.sent {
		if strings.Contains(sent.Text, "fallback inspection limit") || strings.Contains(sent.Text, "Log tail:") {
			t.Fatalf("cancelled run produced a notice detail: %s", sent.Text)
		}
	}
	r := e.record()
	if r.CI == "failed" {
		t.Fatalf("our own cancellation failed CI: %s", r.CIReason)
	}
	for _, check := range r.Checks {
		if check.State == "failure" {
			t.Fatalf("cancelled job saved as a failed check: %+v", check)
		}
	}
}

// One line reports every artifact that the fallback limit left uninspected.
func TestUninspectedReportArtifactsCollapseToOneLine(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	unrelated := testReportZip(t, map[string]string{"notes.xml": "<settings/>"})
	var listed []string
	listed = append(listed,
		fmt.Sprintf(`{"id":90,"name":"junit-report-1","size_in_bytes":%d}`, maxTestArtifact+1),
		fmt.Sprintf(`{"id":91,"name":"junit-report-2","size_in_bytes":%d}`, maxTestArtifact+1),
	)
	for i := 0; i < 5; i++ {
		id := 100 + i
		listed = append(listed, fmt.Sprintf(`{"id":%d,"name":"go-test-raw-%d","size_in_bytes":1024,"created_at":%q}`, id, i, e.clock.Format(time.RFC3339)))
		e.f.Blobs[fmt.Sprintf("repos/owner/repo/actions/artifacts/%d/zip", id)] = unrelated
	}
	e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"] = `HTTP/2.0 200 OK` + "\r\n\r\n" + `{"artifacts":[` + strings.Join(listed, ",") + `]}`
	e.track()
	e.tick()
	var text strings.Builder
	for _, sent := range e.sent {
		text.WriteString(sent.Text)
	}
	const want = "Other CI artifacts: 2 of 5 not inspected (the limit is 3 files and 20 MiB per run)."
	if strings.Count(text.String(), want) != 1 {
		t.Fatalf("missing collapsed report limit line %q:\n%s", want, text.String())
	}
	if strings.Count(text.String(), "Test reports: 2 artifacts over the 20 MiB size limit.") != 1 {
		t.Fatalf("missing collapsed size limit line:\n%s", text.String())
	}
	if strings.Contains(text.String(), "go-test-raw-") {
		t.Fatalf("per-artifact detail remained:\n%s", text.String())
	}
}

// The fallback pool takes three files, so raw test output must win a slot ahead
// of a built bundle. The line names the artifacts it skipped, so no reader
// believes a test report was dropped.
func TestAuxiliaryArtifactsPreferRawTestOutput(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	unrelated := testReportZip(t, map[string]string{"notes.xml": "<settings/>"})
	bundle := testReportZip(t, map[string]string{"index.html": strings.Repeat("x", 4096)})
	e.f.Blobs["repos/owner/repo/actions/artifacts/200/zip"] = bundle
	listed := []string{fmt.Sprintf(`{"id":200,"name":"frontend-dist","size_in_bytes":%d,"created_at":%q}`, len(bundle), e.clock.Format(time.RFC3339))}
	rawIDs := []int64{201, 202, 203}
	for i, id := range rawIDs {
		listed = append(listed, fmt.Sprintf(`{"id":%d,"name":"go-test-raw-%d","size_in_bytes":1024,"created_at":%q}`, id, i, e.clock.Format(time.RFC3339)))
		e.f.Blobs[fmt.Sprintf("repos/owner/repo/actions/artifacts/%d/zip", id)] = unrelated
	}
	e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"] = `HTTP/2.0 200 OK` + "\r\n\r\n" + `{"artifacts":[` + strings.Join(listed, ",") + `]}`
	e.track()
	e.tick()
	var text strings.Builder
	for _, sent := range e.sent {
		text.WriteString(sent.Text)
	}
	const want = "Other CI artifacts: 1 of 4 not inspected (the limit is 3 files and 20 MiB per run)."
	if strings.Count(text.String(), want) != 1 {
		t.Fatalf("missing auxiliary limit line %q:\n%s", want, text.String())
	}
	if strings.Contains(text.String(), "Test reports: 1") {
		t.Fatalf("a skipped bundle was called a test report:\n%s", text.String())
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "artifacts/200/zip") {
		t.Fatalf("the bundle took a fallback slot from raw test output:\n%s", calls)
	}
	for _, id := range rawIDs {
		if !strings.Contains(string(calls), fmt.Sprintf("artifacts/%d/zip", id)) {
			t.Fatalf("raw test output %d was not inspected:\n%s", id, calls)
		}
	}
}

// A rerun keeps the earlier attempt's artifacts in the same list. Only the
// current attempt's files may count as skipped, oversized or inspected, so the
// line reports 1 of 4 here and says nothing about the four old ones.
func TestAuxiliaryCountIgnoresEarlierAttemptArtifacts(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	// A rerun starts at the fixture clock, so anything older than it belongs to
	// the earlier attempt.
	e.f.Runs = []Run{{ID: 81, Attempt: 2, Status: "completed", Conclusion: "success", Event: "pull_request", Name: "CI", Started: e.clock.Format(time.RFC3339)}}
	old := e.clock.Add(-time.Hour).Format(time.RFC3339)
	size := testReportZip(t, map[string]string{"notes.xml": "<settings/>"})
	listed := []string{fmt.Sprintf(`{"id":300,"name":"go-diagnostics-old","size_in_bytes":%d,"created_at":%q}`, 1024, old)}
	listed = append(listed, fmt.Sprintf(`{"id":301,"name":"go-diagnostics-older","size_in_bytes":%d,"created_at":%q}`, maxTestArtifact+1, old))
	for i := 0; i < 3; i++ {
		id := 302 + i
		listed = append(listed, fmt.Sprintf(`{"id":%d,"name":"go-diagnostics-old-%d","size_in_bytes":%d,"created_at":%q}`, id, i, 1024, old))
	}
	for i := 0; i < 4; i++ {
		id := 310 + i
		listed = append(listed, fmt.Sprintf(`{"id":%d,"name":"go-diagnostics-%d","size_in_bytes":%d,"created_at":%q}`, id, i, 1024, e.clock.Format(time.RFC3339)))
		e.f.Blobs[fmt.Sprintf("repos/owner/repo/actions/artifacts/%d/zip", id)] = size
	}
	e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"] = `HTTP/2.0 200 OK` + "\r\n\r\n" + `{"artifacts":[` + strings.Join(listed, ",") + `]}`
	e.track()
	e.tick()
	var text strings.Builder
	for _, sent := range e.sent {
		text.WriteString(sent.Text)
	}
	const want = "Other CI artifacts: 1 of 4 not inspected (the limit is 3 files and 20 MiB per run)."
	if strings.Count(text.String(), want) != 1 {
		t.Fatalf("rerun counted the earlier attempt's artifacts:\n%s", text.String())
	}
	if strings.Contains(text.String(), "20 MiB size limit") {
		t.Fatalf("an earlier attempt's oversized artifact produced a warning:\n%s", text.String())
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "artifacts/300/zip") || strings.Contains(string(calls), "artifacts/310/zip") == false {
		t.Fatalf("an earlier attempt's artifact was inspected, or the current ones were not:\n%s", calls)
	}
}

// A log excerpt drops colors and GitHub's job, step and time prefixes, and
// keeps only the last lines.
func TestLogExcerptStripsColorsAndGitHubPrefixes(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("go-test\tRequire every Go job\t2026-10-23T14:20:%02d.7142798Z line %d", i, i))
	}
	lines = append(lines, "go-test\tRequire every Go job\t2026-10-23T14:21:00.0000000Z \x1b[36;1mA Go job ended with failure\x1b[0m")
	got := failureExcerpt(strings.Join(lines, "\n"), true)
	if strings.ContainsAny(got, "\x1b") || strings.Contains(got, "2026-10-23T") || strings.Contains(got, "go-test\t") {
		t.Fatalf("prefixes remained:\n%q", got)
	}
	if !strings.Contains(got, "A Go job ended with failure") {
		t.Fatalf("final failure line lost:\n%s", got)
	}
	if kept := len(strings.Split(got, "\n")); kept > 30 || strings.Contains(got, "line 0") {
		t.Fatalf("excerpt kept %d lines:\n%s", kept, got)
	}
}
