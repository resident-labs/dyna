package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
)

const budgetWorkflowPath = "repos/owner/repo/contents/.github/workflows/ci.yml?ref=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func budgetWorkflow(yaml string) string {
	return workflowResponse(fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(yaml))))
}
func budgetFixture(e *testEnv, start time.Time, event string) {
	p := RoomPolicy(State{}, e.author.RoomID)
	p.CITimeBudget = "enforce"
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		e.t.Fatal(err)
	}

	e.f.Checks = nil
	e.f.Runs = []Run{{ID: 81, Attempt: 1, Status: "in_progress", Event: event, Name: "CI", Path: ".github/workflows/ci.yml@refs/pull/17/merge", HeadSHA: e.f.Pull.Head.SHA}}
	e.f.Raw = map[string]string{budgetWorkflowPath: budgetWorkflow("jobs:\n  a:\n    name: Go tests\n"), "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100": fmt.Sprintf("HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[{\"name\":\"Go tests\",\"started_at\":%q,\"steps\":[{\"name\":\"Test\",\"status\":\"in_progress\",\"started_at\":%q}]}]}", start.Format(time.RFC3339), start.Format(time.RFC3339))}
}
func TestCIBudgetWarnCancelAndPersist(t *testing.T) {
	e := newEnv(t)
	start := e.clock
	budgetFixture(e, start, "pull_request")
	e.track()
	e.tick()
	if len(e.sent) != 0 {
		t.Fatalf("early warning: %+v", e.sent)
	}
	e.clock = e.clock.Add(30 * time.Second)
	e.tick()
	r := e.record()
	if len(e.sent) != 0 || len(r.Notices) != 0 {
		t.Fatalf("warning woke someone: %+v", e.sent)
	}
	shown, err := e.s.Show(r.Key)
	if err != nil || !strings.Contains(shown.CIBudgetNotes["81/1"], "Go tests / Test (150 seconds)") {
		t.Fatalf("warning note missing from PR status: %+v, %v", shown, err)
	}
	e.tick()
	r = e.record()
	if len(e.sent) == 0 || e.sent[0].Target != e.author.ID || !strings.Contains(e.sent[0].Text, "over CI time budget (3m)") {
		t.Fatalf("budget failure did not wake author: %+v", e.sent)
	}
	if r.CI != "failed" || r.CIReason != "over CI time budget (3m)" {
		t.Fatalf("budget state: %+v", r)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "/actions/runs/81/cancel") != 1 {
		t.Fatalf("cancel calls: %s", calls)
	}
	before := len(e.sent)
	e.f.Runs[0].Status, e.f.Runs[0].Conclusion = "completed", "cancelled"
	e.tick()
	if e.record().CI != "failed" || len(e.sent) != before || e.record().CINoJUnitWarned || e.record().CITestReportNote != "" {
		t.Fatalf("failure was not durable: %+v", e.record())
	}
	// A rerun of the same head is a new budget, not a permanent failed head.
	e.f.Runs[0].Attempt = 2
	e.f.Runs[0].Conclusion = "success"
	e.tick()
	if e.record().CI != "passed" {
		t.Fatalf("rerun did not recover: %+v", e.record())
	}
}
func TestCIBudgetQueueAndPushAreSafe(t *testing.T) {
	for _, event := range []string{"push", "pull_request"} {
		t.Run(event, func(t *testing.T) {
			e := newEnv(t)
			budgetFixture(e, e.clock.Add(-time.Hour), event)
			if event == "pull_request" {
				e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[{\"name\":\"queued\"}]}"
			}
			e.track()
			e.tick()
			if len(e.sent) != 0 || e.record().CI != "pending" {
				t.Fatalf("queued or push run budgeted: %+v", e.record())
			}
			calls, _ := os.ReadFile(e.calls)
			if strings.Contains(string(calls), "/cancel") {
				t.Fatal("cancelled unsafe run")
			}
			if event == "push" && strings.Contains(string(calls), "/jobs?") {
				t.Fatal("fetched push jobs")
			}
		})
	}
}
func TestCIBudgetWarnOnceAndCancelGuard(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-time.Minute), "pull_request")
	e.track()
	e.write()
	p := RoomPolicy(State{}, "")
	p.CITimeBudget = "enforce"
	p.CIFailAfter = "10m"
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r := e.record()
		snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
		if err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(time.Minute)
		if err := e.s.enforceCIBudget(context.Background(), e.s.GH, r, snap, p); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.record().Notices) != 0 || len(e.record().CIBudgetNotes) != 1 {
		t.Fatalf("duplicate warning: %+v", e.record().Notices)
	}
	if err := e.s.GH.CancelRun(context.Background(), refRecord(e.record()), Run{ID: 81, Event: "push"}); err == nil {
		t.Fatal("push cancel accepted")
	}
}

func TestBudgetFailureTracksCurrentHeadAndRun(t *testing.T) {
	old := Run{ID: 1, Attempt: 1, WorkflowID: 9, Name: "CI", Event: "pull_request"}
	r := Record{CIBudgetHead: "head", CIBudgetRuns: map[string]string{"1/1": "over CI time budget (3m)"}}
	snap := Snapshot{Runs: []Run{old}}
	snap.Pull.Head.SHA = "head"
	if reason, _ := budgetFailure(r, snap, ChangePolicy{CITimeBudget: "enforce"}); reason == "" {
		t.Fatal("lost failed attempt")
	}
	newer := old
	newer.ID = 2
	snap.Runs = append(snap.Runs, newer)
	if reason, _ := budgetFailure(r, snap, ChangePolicy{CITimeBudget: "enforce"}); reason != "" {
		t.Fatal("old run blocked replacement run")
	}
	snap.Runs = []Run{old}
	snap.Pull.Head.SHA = "new-head"
	if reason, _ := budgetFailure(r, snap, ChangePolicy{CITimeBudget: "enforce"}); reason != "" {
		t.Fatal("old head blocked new head")
	}
}

func TestCIBudgetIgnoresQueuedJobTimestamp(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-time.Hour), "pull_request")
	path := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	e.f.Raw[path] = strings.Replace(e.f.Raw[path], `"name":"Go tests"`, `"status":"queued","name":"Go tests"`, 1)
	e.track()
	e.tick()
	if len(e.sent) != 0 {
		t.Fatalf("queued job started clock: %+v", e.sent)
	}
}

func TestCIBudgetNewHeadFailsOnFirstPoll(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	budgetFixture(e, e.clock.Add(-3*time.Minute), "pull_request")
	e.tick()
	r := e.record()
	if r.CI != "failed" || r.CIReason != "over CI time budget (3m)" || r.CIBudgetHead != e.f.Pull.Head.SHA {
		t.Fatalf("new head budget lost: %+v", r)
	}
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "Head: "+e.f.Pull.Head.SHA) {
		t.Fatalf("wrong notice head: %+v", e.sent)
	}
}

func TestCIBudgetIgnoresGapsAndOverlap(t *testing.T) {
	for _, tc := range []struct {
		name string
		jobs string
		want string
	}{
		{"queue gap", jobJSON("Go tests", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:05Z") + `,{"status":"queued"}`, ""},
		{"overlap", jobJSON("Go tests", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z") + "," + jobJSON("Go tests", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z"), ""},
		{"separate unrelated jobs", jobJSON("Go tests", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z") + "," + jobJSON("Go tests", "2026-01-01T00:04:00Z", "2026-01-01T00:04:00Z", "2026-01-01T00:05:00Z"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
			budgetFixture(e, e.clock, "pull_request")
			e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + tc.jobs + "]}"
			e.track()
			e.tick()
			if got := e.record().CIBudgetRuns["81/1"]; got != tc.want {
				t.Fatalf("budget = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCIBudgetErrorsKeepSnapshotProcessing(t *testing.T) {
	for _, kind := range []string{"jobs", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			budgetFixture(e, e.clock.Add(-4*time.Minute), "pull_request")
			if kind == "jobs" {
				e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 403 Forbidden\r\n\r\n{\"message\":\"denied\"}"
			} else {
				e.f.CancelError = true
			}
			e.track()
			e.f.Pull.Title = "Updated snapshot title"
			e.tick()
			r := e.record()
			if r.Title != e.f.Pull.Title || (kind == "jobs" && r.CI != "pending") || r.Error != "" {
				t.Fatalf("snapshot blocked: %+v", r)
			}
			if !strings.HasPrefix(r.CIBudgetErrors["81/1"], "budget error:") {
				t.Fatalf("error not recorded: %+v", r.CIBudgetRuns)
			}
		})
	}
}

func TestCIBudgetFinishedOverBudget(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-4*time.Minute), "pull_request")
	e.f.Runs[0].Status = "completed"
	e.f.Runs[0].Conclusion = "success"
	path := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	e.f.Raw[path] = strings.Replace(e.f.Raw[path], `"name":"Go tests"`, fmt.Sprintf(`"status":"completed","completed_at":%q,"name":"Go tests"`, e.clock.Format(time.RFC3339)), 1)
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" || r.CIReason != "over CI time budget (finished)" {
		t.Fatalf("finished run passed: %+v", r)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "run cancel") {
		t.Fatal("cancelled finished run")
	}
}
func TestCIBudgetExemptWorkflowStillGates(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-4*time.Minute), "pull_request")
	e.f.Runs[0].Name = "CodeQL"
	e.track()
	e.write()
	r := e.record()
	snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
	if err != nil {
		t.Fatal(err)
	}
	p := RoomPolicy(State{}, "")
	p.CITimeBudget = "enforce"
	if err := json.Unmarshal([]byte(`{"ciBudgetExempt":["CodeQL"]}`), &p); err != nil {
		t.Fatal(err)
	}
	if err := e.s.enforceCIBudget(context.Background(), e.s.GH, r, snap, p); err != nil {
		t.Fatal(err)
	}
	if len(e.record().CIBudgetRuns) != 0 {
		t.Fatal("exempt workflow budgeted")
	}
	snap.Runs[0].Status = "completed"
	snap.Runs[0].Conclusion = "failure"
	if state, _, _, _ := snap.CIStatus(); state != "failed" {
		t.Fatal("exemption removed CI gate")
	}
}
func TestCIBudgetErrorPreservesWarning(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-2*time.Minute), "pull_request")
	e.track()
	e.write()
	r := e.record()
	snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
	if err != nil {
		t.Fatal(err)
	}
	p := RoomPolicy(State{}, "")
	p.CITimeBudget = "enforce"
	p.CIFailAfter = "10m"
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	enforce := func() {
		t.Helper()
		if err := e.s.enforceCIBudget(context.Background(), e.s.GH, e.record(), snap, p); err != nil {
			t.Fatal(err)
		}
	}
	enforce()
	path := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	raw := e.f.Raw[path]
	e.f.Raw[path] = "HTTP/2.0 403 Forbidden\r\n\r\n{\"message\":\"denied\"}"
	e.write()
	enforce()
	if e.record().CIBudgetRuns["81/1"] != "warned" {
		t.Fatal("budget error overwrote warning")
	}
	e.f.Raw[path] = raw
	e.write()
	enforce()
	if r := e.record(); r.CIBudgetRuns["81/1"] != "warned" || len(r.Notices) != 0 || len(r.CIBudgetNotes) != 1 {
		t.Fatalf("warning repeated: %+v", r)
	}
}
func TestCIBudgetCancelFailureStillFails(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-4*time.Minute), "pull_request")
	e.f.CancelError = true
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" || r.CIReason != "over CI time budget (cancel failed)" {
		t.Fatalf("cancel error hid failure: %+v", r)
	}
	e.f.Runs[0].Status = "completed"
	e.f.Runs[0].Conclusion = "success"
	e.tick()
	if e.record().CI != "failed" {
		t.Fatal("finished run lost durable failure")
	}
}

func jobJSON(name, created, start, end string) string {
	return fmt.Sprintf(`{"name":%q,"status":"completed","created_at":%q,"started_at":%q,"completed_at":%q,"steps":[{"name":"Test","status":"completed","started_at":%q,"completed_at":%q}]}`, name, created, start, end, start, end)
}

// The #515 shape: four jobs ran one after another because runners were busy.
// The longest needs chain is about 128 s, so the run passes and queue wait is recorded.
func TestCIBudgetMeasuresCriticalPathNotQueue(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	budgetFixture(e, e.clock, "pull_request")
	e.f.Raw[budgetWorkflowPath] = budgetWorkflow("jobs:\n  changes: {}\n  frontend:\n    needs: changes\n  test:\n    name: go-test\n    needs: [changes, frontend]\n  computer-smoke:\n    needs: [changes, frontend]\n")
	e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + strings.Join([]string{
		jobJSON("changes", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:06Z"),
		jobJSON("frontend", "2026-01-01T00:00:06Z", "2026-01-01T00:00:06Z", "2026-01-01T00:00:50Z"),
		jobJSON("go-test", "2026-01-01T00:00:50Z", "2026-01-01T00:00:50Z", "2026-01-01T00:01:42Z"),
		jobJSON("computer-smoke", "2026-01-01T00:00:50Z", "2026-01-01T00:01:42Z", "2026-01-01T00:03:00Z"),
	}, ",") + "]}"
	e.track()
	e.tick()
	r := e.record()
	if r.CI == "failed" || r.CIBudgetRuns["81/1"] != "warned" || len(e.sent) != 0 {
		t.Fatalf("serialized run failed the budget: %+v", r)
	}
	if got := r.CIQueueWait["81/1"]; got != "52s" {
		t.Fatalf("queue wait = %q", got)
	}
}

func TestCIBudgetFailsSlowChainAndSingleJob(t *testing.T) {
	for name, tc := range map[string]struct{ workflow, jobs string }{
		"chain":  {"jobs:\n  a: {}\n  b:\n    needs: a\n", jobJSON("a", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:45Z") + "," + jobJSON("b", "2026-01-01T00:01:45Z", "2026-01-01T00:01:45Z", "2026-01-01T00:03:30Z")},
		"single": {"jobs:\n  a: {}\n", jobJSON("a", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:03:10Z")},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
			budgetFixture(e, e.clock, "pull_request")
			e.f.Raw[budgetWorkflowPath] = budgetWorkflow(tc.workflow)
			e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + tc.jobs + "]}"
			e.track()
			e.tick()
			if r := e.record(); r.CI != "failed" || !strings.HasPrefix(r.CIBudgetRuns["81/1"], "over CI time budget") {
				t.Fatalf("slow run passed: %+v", r)
			}
		})
	}
}

func TestCIBudgetMatrixAndDuplicateNames(t *testing.T) {
	for name, tc := range map[string]struct {
		workflow, jobs, want string
	}{
		// Matrix instances are named "build (linux)"; test needs build: 100 s + 100 s chain.
		"matrix chain": {"jobs:\n  build:\n    strategy: {matrix: {os: [linux, mac]}}\n  test:\n    name: go-test\n    needs: build\n",
			jobJSON("build (linux)", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:40Z") + "," + jobJSON("build (mac)", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:30Z") + "," + jobJSON("go-test", "2026-01-01T00:01:40Z", "2026-01-01T00:01:40Z", "2026-01-01T00:03:20Z"), "over CI time budget"},
		// A matrix name built from an expression still maps: 100 s + 100 s chain.
		"expression names": {"jobs:\n  build:\n    name: Build ${{ matrix.os }}\n  test:\n    name: go-test\n    needs: build\n",
			jobJSON("Build linux", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:01:40Z") + "," + jobJSON("go-test", "2026-01-01T00:01:40Z", "2026-01-01T00:01:40Z", "2026-01-01T00:03:20Z"), "over CI time budget"},
		// Two jobs share a display name, so a timing cannot be assigned to one of them.
		// This must be an error, not 130 s on both IDs (260 s chain).
		"duplicate names": {"jobs:\n  a:\n    name: same\n  b:\n    name: same\n    needs: a\n",
			jobJSON("same", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:10Z") + "," + jobJSON("same", "2026-01-01T00:00:10Z", "2026-01-01T00:00:10Z", "2026-01-01T00:02:20Z"), "ERR"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
			budgetFixture(e, e.clock, "pull_request")
			e.f.Raw[budgetWorkflowPath] = budgetWorkflow(tc.workflow)
			e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + tc.jobs + "]}"
			e.track()
			e.tick()
			r := e.record()
			if tc.want == "ERR" {
				if !strings.Contains(r.CIBudgetErrors["81/1"], "cannot map job") {
					t.Fatalf("ambiguous name not reported: %+v", r)
				}
				return
			}
			if got := r.CIBudgetRuns["81/1"]; !strings.HasPrefix(got, tc.want) {
				t.Fatalf("budget = %q, want %q", got, tc.want)
			}
		})
	}
}

// The repository's CI workflow must map each API job name to one workflow job.
func TestCIWorkflowNamesMapToOneJob(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := parseWorkflowJobs(body)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"go-test": "test",
	} {
		var keys []string
		for key, job := range jobs {
			if job.name.MatchString(name) {
				keys = append(keys, key)
			}
		}
		if len(keys) != 1 || keys[0] != want {
			t.Errorf("API job %q maps to %v; want [%s]", name, keys, want)
		}
	}
}

// A workflow rename must not block PRs: unmappable names fall back to busy time.
func TestCIBudgetUnmappableNameFallsBackToBusyTime(t *testing.T) {
	for name, tc := range map[string]struct{ end, want string }{
		"fast run": {"2026-01-01T00:02:30Z", "warned"},
		"slow run": {"2026-01-01T00:03:30Z", "over CI time budget"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
			budgetFixture(e, e.clock, "pull_request")
			e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + jobJSON("renamed", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", tc.end) + "]}"
			e.track()
			e.tick()
			r := e.record()
			if !strings.HasPrefix(r.CIBudgetRuns["81/1"], tc.want) || !strings.Contains(r.CIBudgetErrors["81/1"], "cannot map job") {
				t.Fatalf("no fallback or note: %+v", r)
			}
		})
	}
}

// A saved verdict without matching input evidence must be measured again,
// even if the old record says that runner steps were used.
func TestCIBudgetRemeasuresVerdictWithoutInputEvidence(t *testing.T) {
	for name, tc := range map[string]struct {
		critical  bool
		wantRuns  string
		wantFetch int
	}{
		"legacy":  {false, "warned", 2},
		"current": {true, "warned", 2},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
			budgetFixture(e, e.clock, "pull_request")
			e.f.Raw[budgetWorkflowPath] = budgetWorkflow("jobs:\n  changes: {}\n  frontend:\n    needs: changes\n  test:\n    name: go-test\n    needs: [changes, frontend]\n  computer-smoke:\n    needs: [changes, frontend]\n")
			e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + strings.Join([]string{
				jobJSON("changes", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:06Z"),
				jobJSON("frontend", "2026-01-01T00:00:06Z", "2026-01-01T00:00:06Z", "2026-01-01T00:00:50Z"),
				jobJSON("go-test", "2026-01-01T00:00:50Z", "2026-01-01T00:00:50Z", "2026-01-01T00:01:42Z"),
				jobJSON("computer-smoke", "2026-01-01T00:00:50Z", "2026-01-01T00:01:42Z", "2026-01-01T00:03:00Z"),
			}, ",") + "]}"
			e.track()
			e.write()
			r := e.record()
			snap, err := e.s.GH.Snapshot(context.Background(), refRecord(r))
			if err != nil {
				t.Fatal(err)
			}
			r.CIBudgetHead = snap.Pull.Head.SHA
			r.CIBudgetRuns = map[string]string{"81/1": "over CI time budget (cancel failed)"}
			r.CIBudgetSteps = map[string]bool{"81/1": tc.critical}
			if err := e.s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := e.s.enforceCIBudget(context.Background(), e.s.GH, e.record(), snap, ChangePolicy{CITimeBudget: "enforce"}); err != nil {
					t.Fatal(err)
				}
			}
			if got := e.record().CIBudgetRuns["81/1"]; got != tc.wantRuns {
				t.Fatalf("verdict = %q, want %q", got, tc.wantRuns)
			}
			calls, _ := os.ReadFile(e.calls)
			if got := strings.Count(string(calls), "/attempts/1/jobs"); got != tc.wantFetch {
				t.Fatalf("job fetches = %d, want %d", got, tc.wantFetch)
			}
		})
	}
}

// Busy time adds parallel jobs, so it can only bound the critical path.
// When the workflow cannot be read, the verdict is unknown, never over budget.
func TestCIBudgetUnreadWorkflowIsUnknown(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	budgetFixture(e, e.clock, "pull_request")
	e.f.Raw[budgetWorkflowPath] = "HTTP/2.0 502 Bad Gateway\r\n\r\n{\"message\":\"bad gateway\"}"
	e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[" + jobJSON("a", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:02:00Z") + "," + jobJSON("b", "2026-01-01T00:01:30Z", "2026-01-01T00:01:30Z", "2026-01-01T00:03:30Z") + "]}"
	e.track()
	e.tick()
	r := e.record()
	if r.CI == "failed" || r.CIBudgetRuns["81/1"] != "budget unknown" || r.CIBudgetErrors["81/1"] == "" {
		t.Fatalf("unread workflow judged the budget: runs=%v errors=%v ci=%s", r.CIBudgetRuns, r.CIBudgetErrors, r.CI)
	}
}

// The #1065 and #1315 shapes: the runner spent 119 s in "Set up job" on an
// overloaded host, and its job-started hook waited 200 s in "Set up runner".
// Runner-owned steps are not the pull request's cost, so only "Test" counts.
func TestCIBudgetIgnoresRunnerOwnedSteps(t *testing.T) {
	e := newEnv(t)
	e.clock = time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	budgetFixture(e, e.clock, "pull_request")
	step := func(name, start, end string) string {
		return fmt.Sprintf(`{"name":%q,"status":"completed","started_at":%q,"completed_at":%q}`, name, start, end)
	}
	e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = `HTTP/2.0 200 OK

{"jobs":[{"name":"Go tests","status":"completed","created_at":"2026-01-01T00:00:00Z","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:07:00Z","steps":[` + strings.Join([]string{
		step("Set up job", "2026-01-01T00:00:00Z", "2026-01-01T00:01:59Z"),
		// #1315: the job-started hook waited 200 s for a go-test slot.
		step("Set up runner", "2026-01-01T00:01:59Z", "2026-01-01T00:05:19Z"),
		step("Test", "2026-01-01T00:05:19Z", "2026-01-01T00:05:42Z"),
		step("Complete runner", "2026-01-01T00:05:42Z", "2026-01-01T00:06:00Z"),
		step("Complete job", "2026-01-01T00:06:00Z", "2026-01-01T00:07:00Z"),
	}, ",") + "]}]}"
	e.track()
	e.tick()
	if r := e.record(); r.CI == "failed" || r.CIBudgetRuns["81/1"] != "" || len(e.sent) != 0 {
		t.Fatalf("runner-owned steps counted against the budget: %+v, sent %+v", r, e.sent)
	}
}

func TestCIBudgetSuccessfulReadClearsOnlyRecoveredError(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-117*time.Second), "pull_request")
	e.f.Draft = true
	e.f.Runs[0].Status, e.f.Runs[0].Conclusion = "completed", "success"
	e.write()
	r := e.track()
	snap := Snapshot{Pull: e.f.Pull, Runs: e.f.Runs}
	policy := ChangePolicy{CITimeBudget: "enforce"}
	refresh := func() {
		t.Helper()
		if err := e.s.enforceCIBudget(t.Context(), e.s.GH, e.record(), snap, policy); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	queueWait := e.record().CIQueueWait["81/1"]
	if queueWait != "0s" {
		t.Fatalf("queue wait=%q, want 0s", queueWait)
	}
	path := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	jobs := e.f.Raw[path]
	e.f.Raw[path] = "HTTP/2.0 502 Bad Gateway\r\n\r\n{\"message\":\"bad gateway\"}"
	e.write()
	refresh()
	if e.record().CIBudgetErrors["81/1"] == "" {
		t.Fatal("failed jobs read did not record an error")
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].CIBudgetErrors["82/1"] = "budget error: another run still failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Raw[path] = jobs
	e.write()
	refresh()
	got := e.record()
	if got.CIBudgetErrors["81/1"] != "" {
		t.Fatalf("successful read kept the old error: %s", got.CIBudgetErrors["81/1"])
	}
	if got.CIBudgetErrors["82/1"] == "" {
		t.Fatal("recovery cleared another run's error")
	}
	if got.CIQueueWait["81/1"] != queueWait || !got.IsDraft {
		t.Fatalf("queue wait=%q, draft=%v; want %q, true", got.CIQueueWait["81/1"], got.IsDraft, queueWait)
	}
}
