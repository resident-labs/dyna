package watch

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/cibudget"
)

func testReportZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for name, contents := range entries {
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func testReportFixture(t *testing.T, e *testEnv, entries map[string]string) {
	p := RoomPolicy(State{}, e.author.RoomID)
	p.CITimeBudget = "enforce"
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		e.t.Fatal(err)
	}

	t.Helper()
	data := testReportZip(t, entries)
	e.f.Checks = nil
	e.f.Runs = []Run{{ID: 81, Attempt: 1, Status: "completed", Conclusion: "success", Event: "pull_request", Name: "CI", Started: e.clock.Format(time.RFC3339)}}
	e.f.Raw = map[string]string{"repos/owner/repo/actions/runs/81/artifacts?per_page=100": fmt.Sprintf("HTTP/2.0 200 OK\r\n\r\n{\"artifacts\":[{\"id\":91,\"name\":\"junit-report\",\"size_in_bytes\":%d,\"created_at\":%q}]}", len(data), e.clock.Format(time.RFC3339))}
	e.f.Blobs = map[string][]byte{"repos/owner/repo/actions/artifacts/91/zip": data}
	// A changed test file without a patch makes every reported test the PR's own.
	e.f.Raw["repos/owner/repo/pulls/17/files?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n[{\"filename\":\"web/all.test.js\"}]"
}

func TestTestBudgetFailsHeadAndWakesOnce(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"nested/results.xml": `<testsuites><testsuite><testcase classname="pkg" name="slow" time="12.5"/><testcase name="fast" time="1"/></testsuite></testsuites>`})
	e.track()
	e.tick()
	r := e.record()
	if r.CI != "failed" || r.CIReason != "tests over 10s: pkg/slow" {
		t.Fatalf("state: %+v", r)
	}
	if len(e.sent) == 0 || !strings.Contains(e.sent[0].Text, "12.5s  pkg/slow") {
		t.Fatalf("wake: %+v", e.sent)
	}
	count := len(e.sent)
	e.tick()
	if len(e.sent) != count {
		t.Fatalf("duplicate wake: %+v", e.sent)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "actions/artifacts/91/zip") != 1 {
		t.Fatalf("downloaded repeatedly: %s", calls)
	}
	// A rerun must not reuse artifacts from its previous attempt.
	e.f.Runs[0].Attempt = 2
	e.f.Runs[0].Started = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	if e.record().CI != "passed" {
		t.Fatalf("old report blocked rerun: %+v", e.record())
	}
}

func TestTestBudgetAllowAndCustomLimit(t *testing.T) {
	for _, allowed := range []string{"pkg/slow"} {
		t.Run(allowed, func(t *testing.T) {
			e := newEnv(t)
			testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase classname="pkg" name="slow" time="12"/><testcase name="boundary" time="2"/></testsuite>`})
			e.track()
			e.write()
			p := RoomPolicy(State{}, "")
			p.CITimeBudget = "enforce"
			p.CITestMax = "2s"
			p.CITestAllow = []string{allowed}
			r := e.record()
			snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.s.enforceTestBudget(context.Background(), e.s.GH, r, snap, p); err != nil {
				t.Fatal(err)
			}
			if reason, _ := budgetFailure(e.record(), snap, ChangePolicy{CITimeBudget: "enforce"}); reason != "" {
				t.Fatal(reason)
			}
		})
	}
}

func TestNoJUnitWarnsOncePerPRAndDoesNotFail(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"config.xml": `<config/>`, "note.txt": "text"})
	e.track()
	e.tick()
	want := "CI uploaded no JUnit XML test report, so per-test timing (10 s budget) is not enforced. Upload a JUnit report as an artifact."
	r := e.record()
	if r.CI != "passed" || !r.CINoJUnitWarned || r.CITestReportNote != want {
		t.Fatalf("state: %+v", r)
	}
	if len(e.sent) == 0 || !strings.Contains(e.sent[0].Text, want) {
		t.Fatalf("wake: %+v", e.sent)
	}
	before := len(e.sent)
	e.f.Runs[0].Attempt = 2
	e.f.Runs[0].Started = e.clock.Format(time.RFC3339)
	e.tick()
	if len(e.sent) != before {
		t.Fatalf("warned twice: %+v", e.sent)
	}
}

// A rerun of the failed jobs only uploads little. The reports of the jobs that
// passed stay on the first attempt, so the notice must not claim there is no
// JUnit report.
func TestRerunKeepingFirstAttemptReportsDoesNotWarnNoJUnit(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": `<testsuite><testcase name="fast" time="1"/></testsuite>`})
	e.f.Runs[0].Attempt = 2
	e.f.Runs[0].Started = e.clock.Add(time.Minute).Format(time.RFC3339)
	// An allow entry for a test in the earlier report is not stale.
	p := RoomPolicy(State{}, e.author.RoomID)
	p.CITimeBudget = "enforce"
	p.CITestAllow = []string{"fast"}
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	e.track()
	e.tick()
	r := e.record()
	if r.CINoJUnitWarned || r.CITestReportNote != "" {
		t.Fatalf("rerun with earlier reports warned: %q", r.CITestReportNote)
	}
	for _, n := range e.sent {
		if strings.Contains(n.Text, "no JUnit") {
			t.Fatalf("notice: %q", n.Text)
		}
	}
}

func TestNoJUnitWarningCanBeDisabled(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	e.track()
	e.tick()
	e.write()
	p := RoomPolicy(State{}, "")
	p.CITimeBudget = "enforce"
	disabled := false
	p.CINoJUnitWarning = &disabled
	r := e.record()
	snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.enforceTestBudget(context.Background(), e.s.GH, r, snap, p); err != nil {
		t.Fatal(err)
	}
	if r = e.record(); !r.CINoJUnitWarned || r.CITestReportNote != "" {
		t.Fatalf("disabled warning: %+v", r)
	}
}

func TestReportLimitsAndNonReports(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries map[string]string
		limit   int64
		count   int
		wantErr bool
	}{
		{"valid nested", map[string]string{"../report.xml": `<testsuites><testsuite><testcase name="x" time="11"/></testsuite></testsuites>`}, 1024, 1, false},
		{"other xml", map[string]string{"other.xml": `<settings/>`}, 1024, 0, false},
		{"malformed", map[string]string{"broken.xml": `<testsuite><testcase`}, 1024, 0, true},
		{"oversize", map[string]string{"bomb.xml": strings.Repeat("x", 2048)}, 1024, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := testReportZip(t, tc.entries)
			remaining := tc.limit
			report, junit, err := readTestArtifact(data, &remaining)
			if (err != nil) != tc.wantErr || len(report.Tests) != tc.count || junit != (tc.count > 0 || tc.name == "malformed") {
				t.Fatalf("report=%+v junit=%v err=%v", report, junit, err)
			}
		})
	}
}

func TestTestBudgetSkipsPushAndLargeArtifacts(t *testing.T) {
	for _, event := range []string{"push", "pull_request"} {
		t.Run(event, func(t *testing.T) {
			e := newEnv(t)
			testReportFixture(t, e, nil)
			e.f.Runs[0].Event = event
			e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"] = `HTTP/2.0 200 OK` + "\r\n\r\n" + `{"artifacts":[{"id":91,"size_in_bytes":20971521}]}`
			e.track()
			e.tick()
			calls, _ := os.ReadFile(e.calls)
			if strings.Contains(string(calls), "actions/artifacts/91/zip") {
				t.Fatal("downloaded oversized artifact")
			}
			if event == "push" && strings.Contains(string(calls), "/artifacts?") {
				t.Fatal("inspected push run")
			}
		})
	}
}

func TestTestBudgetRechecksAfterPolicyChangeAndNewHead(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": `<testsuite><testcase name="slow" time="11"/></testsuite>`})
	e.track()
	e.tick()
	e.write()
	p := RoomPolicy(State{}, "")
	p.CITimeBudget = "enforce"
	p.CITestAllow = []string{"slow"}
	r := e.record()
	snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.enforceTestBudget(context.Background(), e.s.GH, r, snap, p); err != nil {
		t.Fatal(err)
	}
	if reason, _ := budgetFailure(e.record(), snap, ChangePolicy{CITimeBudget: "enforce"}); reason != "" {
		t.Fatalf("allowlist did not clear violation: %s", reason)
	}
	snap.Pull.Head.SHA = strings.Repeat("b", 40)
	if reason, _ := budgetFailure(e.record(), snap, ChangePolicy{CITimeBudget: "enforce"}); reason != "" {
		t.Fatalf("old head blocked new head: %s", reason)
	}
}

func TestArtifactErrorsDoNotBlockPRLifecycle(t *testing.T) {
	for _, where := range []string{"listing", "download"} {
		t.Run(where, func(t *testing.T) {
			e := newEnv(t)
			testReportFixture(t, e, map[string]string{"results.xml": `<testsuite/>`})
			if where == "listing" {
				e.f.Raw = nil
				e.f.Fail = "/artifacts?"
			} else {
				e.f.Blobs = nil
				e.f.Fail = "/artifacts/91/zip"
			}
			e.track()
			e.tick()
			if r := e.record(); r.CITestReportNote != "" || len(r.CITestRuns) != 0 || !pendingTestReports(r) {
				t.Fatalf("first report failure was not deferred: %+v", r)
			}
			e.tick()
			e.tick()
			r := e.record()
			if r.CI != "passed" || r.Error != "" || !strings.Contains(r.CITestReportNote, "report unreadable") {
				t.Fatalf("optional error blocked state: %+v", r)
			}
			before := len(e.sent)
			e.tick()
			if len(e.sent) != before {
				t.Fatalf("report error repeated: %+v", e.sent)
			}
			e.f.Pull.State = "closed"
			e.f.Pull.Merged = true
			e.tick()
			if e.record().State != "merged" {
				t.Fatalf("merge detection blocked: %+v", e.record())
			}
		})
	}
}

func TestSlowTestDoesNotHideFailureLogs(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": `<testsuite><testcase name="slow" time="12"/></testsuite>`})
	e.f.Runs[0].Conclusion = "failure"
	e.f.Log = "original assertion failed at failing_test.go:42"
	e.track()
	e.tick()
	var messages strings.Builder
	for _, sent := range e.sent {
		messages.WriteString(sent.Text)
	}
	if !strings.Contains(messages.String(), "original assertion failed") || !strings.Contains(messages.String(), "tests over 10s: slow") {
		t.Fatalf("lost failure or timing: %s", messages.String())
	}
	if e.record().CI != "failed" {
		t.Fatal("failed run passed")
	}
}

func TestMalformedJUnitIsUnreadablePerAttempt(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"broken.xml": `<testsuite><testcase`})
	e.track()
	e.tick()
	if !strings.Contains(e.record().CITestReportNote, "report unreadable: broken.xml") || e.record().CINoJUnitWarned {
		t.Fatalf("malformed report counted as missing: %+v", e.record())
	}
	before := len(e.sent)
	testReportFixture(t, e, map[string]string{"broken.xml": `<testsuite><testcase`})
	e.f.Runs[0].Attempt = 2
	e.f.Runs[0].Started = e.clock.Format(time.RFC3339)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	if len(e.sent) != before+1 {
		t.Fatalf("new attempt did not report corrupt XML: %+v", e.sent)
	}
}

func TestNonXMLDoesNotConsumeExpansionBudget(t *testing.T) {
	data := testReportZip(t, map[string]string{"huge.txt": strings.Repeat("x", 10000), "report.xml": `<testsuite><testcase name="x" time="1"/></testsuite>`})
	remaining := int64(1000)
	report, junit, err := readTestArtifact(data, &remaining)
	if err != nil || !junit || len(report.Tests) != 1 {
		t.Fatalf("unrelated entry starved report: %+v %v %v", report, junit, err)
	}
}

func TestInvalidStoredTestBudgetIsOptional(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	e.track()
	e.write()
	r := e.record()
	snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
	if err != nil {
		t.Fatal(err)
	}
	policy := RoomPolicy(State{}, "")
	policy.CITimeBudget = "enforce"
	policy.CITestMax = "broken"
	if err := e.s.enforceTestBudget(context.Background(), e.s.GH, r, snap, policy); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.record().CITestReportNote, "invalid stored test budget") {
		t.Fatalf("missing policy note: %+v", e.record())
	}
}

func TestPushRunDoesNotHidePullRequestReport(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": `<testsuite><testcase name="slow" time="11"/></testsuite>`})
	push := e.f.Runs[0]
	push.ID = 99
	push.Event = "push"
	e.f.Runs = append(e.f.Runs, push)
	e.track()
	e.tick()
	if e.record().CI != "failed" || e.record().CIReason != "tests over 10s: slow" {
		t.Fatalf("push hid pull request timing: %+v", e.record())
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "runs/99/artifacts") {
		t.Fatalf("fetched push artifacts: %s", calls)
	}
}

func TestParentDurationsDoNotFailLeafTests(t *testing.T) {
	e := newEnv(t)
	var source cibudget.Report
	if err := source.ReadGo(strings.NewReader(`{"Action":"pass","Package":"pkg","Test":"parent/one","Elapsed":8}
{"Action":"pass","Package":"pkg","Test":"parent/two","Elapsed":8}
{"Action":"pass","Package":"pkg","Test":"parent","Elapsed":20}`)); err != nil {
		t.Fatal(err)
	}
	var xml bytes.Buffer
	if err := source.WriteJUnit(&xml); err != nil {
		t.Fatal(err)
	}
	testReportFixture(t, e, map[string]string{"report.xml": xml.String()})
	e.track()
	e.tick()
	if e.record().CI != "passed" || e.record().CIReason != "" {
		t.Fatalf("parent aggregate failed leaf budget: %+v", e.record())
	}
}

func TestGoParentSetupKeepsItsBudgetThroughJUnit(t *testing.T) {
	var source cibudget.Report
	if err := source.ReadGo(strings.NewReader(`{"Action":"pass","Package":"pkg","Test":"parent/one","Elapsed":1}
{"Action":"pass","Package":"pkg","Test":"parent/two","Elapsed":1}
{"Action":"pass","Package":"pkg","Test":"parent","Elapsed":30}`)); err != nil {
		t.Fatal(err)
	}
	var xml bytes.Buffer
	if err := source.WriteJUnit(&xml); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": xml.String()})
	e.track()
	e.tick()
	if e.record().CI != "failed" || e.record().CIReason != "tests over 10s: pkg/parent" {
		t.Fatalf("slow parent setup escaped: %+v", e.record())
	}
}

func TestLiteralJUnitSlashNamesKeepTheirBudgets(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": `<testsuite><testcase name="a" time="11"/><testcase name="a/b" time="1"/></testsuite>`})
	e.track()
	e.tick()
	if e.record().CI != "failed" || e.record().CIReason != "tests over 10s: a" {
		t.Fatalf("literal JUnit name escaped: %+v", e.record())
	}
}

func TestNoJUnitSilentWhenTestJobSkippedOrCancelled(t *testing.T) {
	for _, tc := range []struct {
		name string
		test string
		want bool
	}{{"skipped", "skipped", false}, {"cancelled", "cancelled", false}, {"passed", "success", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			testReportFixture(t, e, nil)
			e.f.Checks = []Check{
				actionsCheck(Check{ID: 1, Name: "changes", Status: "completed", Conclusion: "success"}),
				actionsCheck(Check{ID: 2, Name: "go-test", Status: "completed", Conclusion: tc.test}),
			}
			e.track()
			e.tick()
			r := e.record()
			if r.CINoJUnitWarned != tc.want || (r.CITestReportNote != "") != tc.want {
				t.Fatalf("warned=%v note=%q, want warning %v", r.CINoJUnitWarned, r.CITestReportNote, tc.want)
			}
		})
	}
}

func TestNoJUnitSilentForCancelledWorkflow(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	e.f.Runs[0].Conclusion = "cancelled"
	// GitHub may return no jobs yet, or a completed setup job from the run.
	e.f.Checks = []Check{actionsCheck(Check{ID: 1, Name: "setup", Status: "completed", Conclusion: "success"})}
	e.track()
	e.tick()
	r := e.record()
	if r.CINoJUnitWarned || r.CITestReportNote != "" {
		t.Fatalf("cancelled run warned: %q", r.CITestReportNote)
	}
	// A cancelled run must not consume the once-per-PR warning for a later
	// completed test run that really failed to upload its report.
	e.f.Runs[0].Attempt = 2
	e.f.Runs[0].Conclusion = "success"
	e.tick()
	if r := e.record(); !r.CINoJUnitWarned || r.CITestReportNote == "" {
		t.Fatal("completed run did not warn after cancellation")
	}
}

func TestTestReportsIgnoreRunsFromOldPRHead(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, nil)
	e.f.Runs[0].HeadSHA = "old-head"
	e.track()
	e.tick()
	r := e.record()
	if r.CINoJUnitWarned || r.CITestReportNote != "" || len(r.CITestRuns) != 0 {
		t.Fatalf("old head produced test report state: %+v", r)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "actions/runs/81/artifacts") {
		t.Fatal("requested artifacts for an old PR head")
	}
}

func TestOldHeadRunCannotHideCurrentTestReport(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase name="fast" time="1"/></testsuite>`})
	e.f.Runs[0].HeadSHA = e.f.Pull.Head.SHA
	old := e.f.Runs[0]
	old.ID = 82
	old.HeadSHA = "old-head"
	e.f.Runs = append(e.f.Runs, old)
	e.track()
	e.tick()
	r := e.record()
	if r.CITestRuns["81/1"] != "checked" || r.CITestRuns["82/1"] != "" || r.CITestReportNote != "" || r.CINoJUnitWarned {
		t.Fatalf("wrong test reports selected: runs=%v note=%q", r.CITestRuns, r.CITestReportNote)
	}
}

// A saved current-head record must not make an explicitly old run enforceable.
func TestTestBudgetFailureIgnoresExplicitOldHeadRun(t *testing.T) {
	snap := Snapshot{Runs: []Run{{ID: 81, Attempt: 1, Event: "pull_request", HeadSHA: "old-head"}}}
	snap.Pull.Head.SHA = "current-head"
	r := Record{CITestHead: "current-head", CITestRuns: map[string]string{"81/1": "tests over 10s: slow"}}
	if reason, _ := budgetFailure(r, snap, ChangePolicy{CITimeBudget: "enforce"}); reason != "" {
		t.Fatalf("old-head run enforced a test budget: %s", reason)
	}
}

func TestReportArtifactsBoundCustomNameDiscovery(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"report.xml": `<testsuite><testcase name="slow" time="12"/></testsuite>`})
	unrelated := testReportZip(t, map[string]string{"settings.xml": "unrelated"})
	e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"] = fmt.Sprintf("HTTP/2.0 200 OK\r\n\r\n{\"artifacts\":[{\"id\":90,\"name\":\"screenshots\",\"size_in_bytes\":%d},{\"id\":91,\"name\":\"reports\",\"size_in_bytes\":%d}]}", maxTestArtifact+1, len(e.f.Blobs["repos/owner/repo/actions/artifacts/91/zip"]))
	e.f.Blobs["repos/owner/repo/actions/artifacts/90/zip"] = unrelated
	e.track()
	e.tick()
	if e.record().CIReason != "tests over 10s: slow" {
		t.Fatalf("unrelated XML starved report: %+v", e.record())
	}
	calls, _ := os.ReadFile(e.calls)
	text := string(calls)
	if !strings.Contains(text, "actions/artifacts/91/zip") || strings.Contains(text, "actions/artifacts/90/zip") {
		t.Fatalf("wrong artifacts downloaded: %s", text)
	}
}
