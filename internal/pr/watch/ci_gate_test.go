package watch

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func actionsCheck(c Check) Check {
	c.App.Slug = "github-actions"
	return c
}

func TestReviewBotCannotEstablishCI(t *testing.T) {
	bot := Check{ID: 1, Name: "Review", Status: "completed", Conclusion: "failure"}
	bot.App.Slug = "greptile"
	external := bot
	external.Name, external.App.Slug = "security", "scanner"
	for _, tc := range []struct {
		name     string
		snap     Snapshot
		required []string
		want     string
	}{
		{"greptile only", Snapshot{Checks: []Check{bot}}, nil, "pending"},
		{"greptile without workflows", Snapshot{Checks: []Check{bot}, NoPRWorkflows: true}, nil, "absent"},
		{"actions pending", Snapshot{Checks: []Check{actionsCheck(Check{Status: "queued"})}}, nil, "pending"},
		{"actions passed ignores bot failure", Snapshot{Checks: []Check{actionsCheck(Check{Status: "completed", Conclusion: "success"}), bot}}, nil, "passed"},
		{"workflow pending", Snapshot{Runs: []Run{{Status: "in_progress"}}}, nil, "pending"},
		{"required external fails", Snapshot{Checks: []Check{external}}, []string{"security"}, "failed"},
		{"unrequired external ignored", Snapshot{Checks: []Check{external}, NoPRWorkflows: true}, nil, "absent"},
		{"required status pending", Snapshot{Statuses: []Status{{Context: "buildkite", State: "pending"}}}, []string{"buildkite"}, "pending"},
		{"required status passed", Snapshot{Statuses: []Status{{Context: "buildkite", State: "success"}}}, []string{"buildkite"}, "passed"},
		{"other status ignored", Snapshot{Statuses: []Status{{Context: "copilot", State: "failure"}}, Runs: []Run{{Status: "completed", Conclusion: "success"}}}, nil, "passed"},
		{"local scope does not invent CI", Snapshot{NoPRWorkflows: true}, []string{"go-test-touched"}, "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, _ := tc.snap.CIStatus(tc.required...)
			if got != tc.want {
				t.Fatalf("CI = %s, want %s", got, tc.want)
			}
		})
	}
	green := Snapshot{Checks: []Check{actionsCheck(Check{ID: 5, Status: "completed", Conclusion: "success"})}}
	_, before, _, _ := green.CIStatus()
	green.Checks = append(green.Checks, bot)
	_, after, _, _ := green.CIStatus()
	if before != after {
		t.Fatal("bot changed the CI key")
	}
}

func TestSameNamedChecksInDifferentWorkflowsNeedSeparatePassesHostedOnly(t *testing.T) {
	pending := actionsCheck(Check{ID: 1, Name: "build", Status: "in_progress"})
	pending.CheckSuite.ID = 1
	passed := actionsCheck(Check{ID: 2, Name: "build", Status: "completed", Conclusion: "success"})
	passed.CheckSuite.ID = 2
	snap := Snapshot{Checks: []Check{pending, passed}}
	state, _, _, _ := snap.CIStatus()
	if state != "pending" {
		t.Fatalf("one workflow's pass replaced another workflow's pending check: %s", state)
	}
	if summaries := snap.CheckSummaries(Ref{}); len(summaries) != 2 {
		t.Fatalf("the PR view hid a same-named workflow check: %+v", summaries)
	}
}

// An external CI app reruns a required check in a new suite. Only its latest
// result per app and name judges the head, so a successful rerun clears an
// older failure instead of the gate holding both suites against it.
func TestExternalCheckRerunKeepsOnlyTheLatestResult(t *testing.T) {
	external := Check{ID: 1, Name: "security", Status: "completed", Conclusion: "failure"}
	external.App.Slug = "scanner"
	external.CheckSuite.ID = 1
	rerun := external
	rerun.ID, rerun.Conclusion, rerun.CheckSuite.ID = 2, "success", 2
	state, _, _, _ := (Snapshot{Checks: []Check{external, rerun}}).CIStatus("security")
	if state != "passed" {
		t.Fatalf("an old failure outlived its successful rerun: %s", state)
	}
}

// A review bot's status never counts as CI: the reviewer starts at once, but
// Dyna reports ready only after real CI passes.
func TestFakeGHReviewBotDoesNotPassCI(t *testing.T) {
	e := newEnv(t)
	e.f.Checks = nil
	e.f.Runs = nil
	e.f.Statuses = []Status{{ID: 1, Context: "CodeRabbit", State: "success", Creator: User{Login: "coderabbitai[bot]", Type: "Bot"}}}
	e.write()
	e.track()
	if err := e.s.Configure(e.author.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r := e.record()
	if r.CI != "pending" || len(e.launches) != 1 {
		t.Fatalf("bot-only CI: %+v launches=%v", r, e.launches)
	}
	if len(r.Checks) != 1 || r.Checks[0].State != "success" {
		t.Fatalf("bot check display lost: %+v", r.Checks)
	}
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Checked file.go:1", 0); err != nil {
		t.Fatal(err)
	}
	ready := func() bool {
		for _, n := range e.sent {
			if strings.Contains(n.Text, "is ready to land") {
				return true
			}
		}
		return false
	}
	e.f.Checks = []Check{actionsCheck(Check{ID: 2, Name: "go-test", Status: "completed", Conclusion: "failure"})}
	e.tick()
	if e.record().CI != "failed" || ready() || !strings.Contains(e.sent[len(e.sent)-1].Text, "test") {
		t.Fatalf("test failure not reported: %+v", e.record())
	}
	e.f.Checks[0].ID = 3
	e.f.Checks[0].Conclusion = "success"
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	if e.record().CI != "passed" || !ready() || len(e.launches) != 1 {
		t.Fatalf("real CI did not make the approval ready: %+v", e.record())
	}
}

func TestAbsentCIStartsReviewAndAcceptsVerdict(t *testing.T) {
	e := newEnv(t)
	e.f.Raw = map[string]string{"repos/owner/repo/actions/workflows?per_page=100": workflowResponse(`{"workflows":[]}`)}
	e.f.Checks, e.f.Runs, e.f.Statuses = nil, nil, nil
	e.write()
	r := e.track()
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r = e.record()
	if r.CI != "absent" || r.Reviewer == "" || len(e.launches) != 1 {
		t.Fatalf("absent CI did not assign review: %+v launches=%v", r, e.launches)
	}
	e.tick()
	if len(e.launches) != 1 {
		t.Fatal("absent CI duplicated assignment")
	}
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Read the diff", 0); err != nil {
		t.Fatal(err)
	}
	if e.record().Verdict != "approve" {
		t.Fatal("absent CI verdict was not stored")
	}
}

func TestWorkflowPREvents(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"on: pull_request", true},
		{"on: [push, pull_request]", true},
		{"on: {pull_request: {branches: [main]}}", true},
		{"on:\n  pull_request_target:\n    types: [opened]", true},
		{"'on':\n  push:", false},
		{"on: workflow_dispatch\n# on: pull_request", false},
		{"on: push\njobs:\n  test:\n    name: pull_request", false},
	} {
		got, err := workflowPREvent([]byte(tc.body))
		if err != nil || got != tc.want {
			t.Fatalf("%q = %v, %v; want %v", tc.body, got, err, tc.want)
		}
	}
	if _, err := workflowPREvent([]byte("on: [")); err == nil {
		t.Fatal("accepted broken YAML")
	}
}

func workflowResponse(body string) string {
	return "HTTP/2.0 200 OK\r\nETag: \"workflow\"\r\n\r\n" + body
}

func TestNoPRWorkflowsAndGraceWindow(t *testing.T) {
	// Share the session fixture. Reset the PR store, clock, and workflow
	// answer for each case.
	e := newEnv(t)
	reset := func(t *testing.T) {
		t.Helper()
		e.f = defaultFixture()
		e.clock = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
		e.s.Store = &Store{Root: filepath.Join(t.TempDir(), "prs")}
		// The workflow capability is cached per executable and repository, so a
		// case with another answer must not read the previous case's result.
		workflowCapabilities.Lock()
		delete(workflowCapabilities.repos, e.s.GH.Executable+":github.com/owner/repo")
		workflowCapabilities.Unlock()
	}
	for _, tc := range []struct {
		name, workflows, body string
		immediate             bool
	}{
		{"disabled", `{"workflows":[{"state":"disabled_manually","path":".github/workflows/ci.yml"}]}`, "", true},
		{"empty", `{"workflows":[]}`, "", true},
		{"push only", `{"workflows":[{"state":"active","path":".github/workflows/ci.yml"}]}`, "on: push", true},
		{"pull request", `{"workflows":[{"state":"active","path":".github/workflows/ci.yml"}]}`, "on: pull_request", false},
		{"unknown", "", "", false},
	} {
		for _, bots := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bots=%v", tc.name, bots), func(t *testing.T) {
				reset(t)
				e.f.Checks, e.f.Runs, e.f.Statuses = nil, nil, nil
				if bots {
					e.f.Statuses = []Status{{Context: "CodeRabbit", State: "success"}}
				}
				if tc.workflows != "" {
					e.f.Raw["repos/owner/repo/actions/workflows?per_page=100"] = workflowResponse(tc.workflows)
				} else {
					e.f.Fail = "/actions/workflows?"
				}
				if tc.body != "" {
					e.f.Raw["repos/owner/repo/contents/.github/workflows/ci.yml"] = workflowResponse(fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(tc.body))))
				}
				e.write()
				ref := Ref{"github.com", "owner/repo", 17}
				active, workflowErr := e.s.GH.hasPRWorkflows(context.Background(), ref)
				snap := Snapshot{NoPRWorkflows: workflowErr == nil && !active}
				r := Record{Key: ref.Key(), Host: ref.Host, Repo: ref.Repo, Number: ref.Number, RoomID: e.author.RoomID, Author: e.author.ID, Head: e.f.Pull.Head.SHA, HeadSeen: e.clock, State: "open", Watching: true}
				if err := e.s.Store.update(func(st *State) error {
					st.Records[r.Key] = &r
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				// Discover workflows once, then exercise the real CI and stored
				// state transitions without refetching unrelated PR endpoints.
				process := func() {
					e.clock = e.clock.Add(time.Minute)
					snap.Pull, snap.Checks, snap.Statuses = e.f.Pull, e.f.Checks, e.f.Statuses
					ci, key, jobs, _ := e.s.ciStatus(snap, e.record(), RoomPolicy(State{}, ""))
					if err := e.s.process(context.Background(), r.Key, snap, ci, key, jobs, nil, false); err != nil {
						t.Fatal(err)
					}
				}
				process()
				want := "pending"
				if tc.immediate {
					want = "absent"
				}
				if got := string(e.record().CI); got != want {
					t.Fatalf("fresh CI = %s, want %s", got, want)
				}
				e.clock = r.HeadSeen.Add(5 * time.Minute)
				process()
				if got := e.record(); got.CI != "absent" || got.CIReason == "" {
					t.Fatalf("expired CI = %s, reason = %q", got.CI, got.CIReason)
				}
				if !tc.immediate && e.record().CIReason != "No CI ran for this change within 5 minutes." {
					t.Fatalf("silent workflow reason = %q", e.record().CIReason)
				}
				// Read from a new service value to use the stored timestamp.
				reloaded := *e.s
				e.s = &reloaded
				for _, state := range []string{"queued", "failure", "success"} {
					check := Check{ID: 1, Name: "tests", Status: "completed", Conclusion: state}
					want := map[string]string{"queued": "pending", "failure": "failed", "success": "passed"}[state]
					if state == "queued" {
						check.Status = state
						check.Conclusion = ""
					}
					e.f.Checks = []Check{actionsCheck(check)}
					e.clock = e.clock.Add(6 * time.Minute)
					process()
					if got := string(e.record().CI); got != want {
						t.Fatalf("late %s CI = %s, want %s", state, got, want)
					}
				}
				e.f.Checks = nil
				e.f.Pull.Head.SHA = strings.Repeat("b", 40)
				process()
				if !tc.immediate && e.record().CI != "pending" {
					t.Fatal("new head inherited elapsed grace")
				}
			})
		}
	}
}

func TestWorkflowCapabilityCacheAndETag(t *testing.T) {
	e := newEnv(t)
	e.f.Checks, e.f.Runs = nil, nil
	e.write()
	ref := Ref{"github.com", "owner/repo", 17}
	if _, err := e.s.GH.Snapshot(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	ref.Number++
	if _, err := e.s.GH.Snapshot(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(e.calls)
	if n := strings.Count(string(calls), "/actions/workflows?"); n != 1 {
		t.Fatalf("repo cache: %d requests", n)
	}
	e.clock = e.clock.Add(5 * time.Minute)
	if _, err := e.s.GH.Snapshot(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(e.calls)
	var validated bool
	for _, call := range strings.Split(string(calls), "\n") {
		if strings.Contains(call, "/actions/workflows?") && strings.Contains(call, "If-None-Match:") {
			validated = true
		}
	}
	if !validated {
		t.Fatal("expired capability did not reuse ETag")
	}
}
