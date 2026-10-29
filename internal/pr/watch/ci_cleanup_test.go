package watch

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
)

func TestTerminalPRCleansUpRunsOnce(t *testing.T) {
	for _, merged := range []bool{true, false} {
		name := "closed"
		if merged {
			name = "merged"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.f.Pull.Head.Ref = "feature/a&b"
			e.write()
			e.track()
			e.s.CleanupMerged = func(context.Context, Record, string) error { return nil }
			e.f.Pull.State, e.f.Pull.Merged = "closed", merged
			for i, status := range []string{"queued", "in_progress", "waiting", "requested", "pending", "completed"} {
				e.f.Runs = append(e.f.Runs, Run{ID: int64(100 + i), HeadBranch: e.f.Pull.Head.Ref, Event: "pull_request", Status: status})
			}
			e.f.Runs = append(e.f.Runs,
				Run{ID: 200, HeadBranch: "main", Event: "push", Status: "queued"},
				Run{ID: 201, HeadBranch: e.f.Pull.Head.Ref, Event: "push", Status: "in_progress"},
				Run{ID: 202, HeadBranch: "other", Event: "pull_request", Status: "queued"},
			)
			e.f.Raw = map[string]string{
				"repos/owner/repo/actions/runs/100/cancel": "HTTP/2.0 202 Accepted\r\n\r\n",
			}
			e.tick()
			r := e.record()
			if r.Watching || !r.CICleanupDone || string(r.State) != name {
				t.Fatalf("unexpected record: %+v", r)
			}
			calls, _ := os.ReadFile(e.calls)
			if got := strings.Count(string(calls), "/cancel"); got != 5 {
				t.Fatalf("got %d cancel calls: %s", got, calls)
			}
			if !strings.Contains(string(calls), "branch=feature%2Fa%26b&event=pull_request") {
				t.Fatalf("missing escaped branch query: %s", calls)
			}
			for _, id := range []string{"105", "200", "201", "202"} {
				if strings.Contains(string(calls), "/runs/"+id+"/cancel") {
					t.Fatalf("canceled protected run %s", id)
				}
			}
			e.tick()
			if err := e.s.process(context.Background(), r.Key, Snapshot{Pull: e.f.Pull}, "", "", nil, nil, false); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(e.calls)
			if strings.Count(string(after), "/cancel") != 5 {
				t.Fatalf("cleanup repeated: %s", after)
			}
		})
	}
}

func TestCICleanupProtectsUntrackedOpenPR(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Ref = "shared"
	e.write()
	e.track()
	other := e.f.Pull
	other.Number = 99
	e.f.OpenPulls = []Pull{other}
	e.f.Pull.State = "closed"
	e.f.Runs = []Run{{ID: 100, HeadBranch: "shared", Event: "pull_request", Status: "queued"}}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "/cancel") {
		t.Fatalf("canceled shared branch: %s", calls)
	}
	if !e.record().CICleanupDone {
		t.Fatal("cleanup attempt was not saved")
	}
}

func TestCICleanupFailureDoesNotBlock(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Ref = "feature"
	e.write()
	e.track()
	e.f.Pull.State, e.f.Pull.Merged = "closed", true
	cleaned := false
	e.s.CleanupMerged = func(context.Context, Record, string) error { cleaned = true; return nil }
	e.f.Runs = []Run{
		{ID: 100, HeadBranch: "feature", Event: "pull_request", Status: "queued"},
		{ID: 101, HeadBranch: "feature", Event: "pull_request", Status: "in_progress"},
	}
	e.f.Fail = "/runs/100/cancel"
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	e.tick()
	if !cleaned || e.record().Watching || !e.record().CICleanupDone {
		t.Fatal("cancel failure blocked the merge path")
	}
	e.tick()
	if strings.Count(logs.String(), "PR CI cleanup") != 1 {
		t.Fatalf("expected one failure log: %s", logs.String())
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "/cancel") != 2 {
		t.Fatalf("cancel failure stopped later runs or repeated: %s", calls)
	}
}

func TestCICleanupLocalChangeDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.s.cleanupCIRuns(context.Background(), Record{Kind: "local", Branch: "feature"}, "")
	if calls, _ := os.ReadFile(e.calls); len(calls) != 0 {
		t.Fatalf("local change called GitHub: %s", calls)
	}
}

func TestCICleanupChecksAllOpenPRPages(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Ref = "shared"
	e.write()
	e.track()
	e.f.Pull.State = "closed"
	e.f.Raw = map[string]string{
		"repos/owner/repo/pulls?state=open&per_page=100":        "HTTP/2.0 200 OK\r\nLink: <https://api.github.com/repos/owner/repo/pulls?state=open&per_page=100&page=2>; rel=\"next\"\r\n\r\n[]",
		"repos/owner/repo/pulls?state=open&per_page=100&page=2": "HTTP/2.0 200 OK\r\n\r\n[{\"number\":99,\"head\":{\"ref\":\"shared\"}}]",
	}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "/cancel") || !strings.Contains(string(calls), "per_page=100&page=2") {
		t.Fatalf("did not protect PR on later page: %s", calls)
	}
}

func TestCICleanupCancelsAllRunPages(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Ref = "feature"
	e.write()
	e.track()
	e.f.Pull.State = "closed"
	e.f.Raw = map[string]string{
		"repos/owner/repo/actions/runs?branch=feature&event=pull_request&per_page=100":        "HTTP/2.0 200 OK\r\nLink: <https://api.github.com/repos/owner/repo/actions/runs?branch=feature&event=pull_request&per_page=100&page=2>; rel=\"next\"\r\n\r\n{\"workflow_runs\":[{\"id\":100,\"head_branch\":\"feature\",\"event\":\"pull_request\",\"status\":\"queued\"}]}",
		"repos/owner/repo/actions/runs?branch=feature&event=pull_request&per_page=100&page=2": "HTTP/2.0 200 OK\r\n\r\n{\"workflow_runs\":[{\"id\":101,\"head_branch\":\"feature\",\"event\":\"pull_request\",\"status\":\"in_progress\"}]}",
	}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "/cancel") != 2 {
		t.Fatalf("missed run page: %s", calls)
	}
}

func TestCICleanupSkipsCancelWhenOpenPRCheckFails(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Ref = "feature"
	e.write()
	e.track()
	e.f.Pull.State = "closed"
	e.f.Fail = "/pulls?"
	e.f.Runs = []Run{{ID: 100, HeadBranch: "feature", Event: "pull_request", Status: "queued"}}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "/cancel") || !e.record().CICleanupDone {
		t.Fatalf("cleanup did not fail safely: %s", calls)
	}
}

func TestHeadChangeCleansUpOldRunsOnce(t *testing.T) {
	for _, mode := range []string{"normal", "shared", "cancel failure", "default branch"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			branch := "feature"
			e.f.Pull.Base.Repo.DefaultBranch = "trunk"
			if mode == "default branch" {
				branch = "trunk"
			}
			e.f.Pull.Head.Ref = branch
			e.write()
			e.track()
			oldHead := e.f.Pull.Head.SHA
			newHead := strings.Repeat("b", 40)
			e.f.Pull.Head.SHA = newHead
			e.f.Runs = nil
			for i, status := range []string{"queued", "in_progress", "waiting", "requested", "pending", "completed"} {
				e.f.Runs = append(e.f.Runs, Run{ID: int64(300 + i), HeadBranch: branch, HeadSHA: oldHead, Event: "pull_request", Status: status})
			}
			e.f.Runs = append(e.f.Runs,
				Run{ID: 400, HeadBranch: branch, HeadSHA: newHead, Event: "pull_request", Status: "queued"},
				Run{ID: 401, HeadBranch: branch, HeadSHA: oldHead, Event: "push", Status: "queued"},
				Run{ID: 402, HeadBranch: "other", HeadSHA: oldHead, Event: "pull_request", Status: "queued"},
				Run{ID: 403, HeadBranch: branch, Event: "pull_request", Status: "queued"},
			)
			if mode == "shared" {
				other := e.f.Pull
				other.Number = 99
				e.f.OpenPulls = []Pull{other}
			}
			if mode == "cancel failure" {
				e.f.Fail = "/runs/300/cancel"
			}
			e.tick()
			r := e.record()
			if r.Head != newHead || r.CIStaleCleanupHead != newHead || !r.Watching {
				t.Fatalf("head change was blocked: %+v", r)
			}
			calls, _ := os.ReadFile(e.calls)
			want := 5
			if mode == "shared" || mode == "default branch" {
				want = 0
			}
			if strings.Count(string(calls), "/cancel") != want {
				t.Fatalf("unexpected cancels: %s", calls)
			}
			for _, id := range []string{"305", "400", "401", "402", "403"} {
				if strings.Contains(string(calls), "/runs/"+id+"/cancel") {
					t.Fatalf("canceled protected run %s: %s", id, calls)
				}
			}
			e.tick()
			after, _ := os.ReadFile(e.calls)
			if strings.Count(string(after), "/cancel") != want {
				t.Fatalf("cleanup repeated for the same head: %s", after)
			}
			if mode == "normal" {
				// Terminal cleanup still cancels the current head after stale cleanup.
				e.f.Pull.State = "closed"
				e.tick()
				after, _ = os.ReadFile(e.calls)
				if !e.record().CICleanupDone || !strings.Contains(string(after), "/runs/400/cancel") {
					t.Fatalf("terminal cleanup skipped current head: %s", after)
				}
			}
		})
	}
}
