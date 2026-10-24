package roomsvc

import (
	"context"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/parentoutput"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/terminalsvc"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFeedShowsFullCompletionAddressedToParent: a child's completion goes to
// its parent, stays visible to the owner, and the feed
// carries the whole result while the stored post keeps its short preview.
func TestFeedShowsFullCompletionAddressedToParent(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	long := strings.Repeat("word ", 400) + "THE-END"
	e.as(&lead)
	child, _, err := e.mgr.Start(context.Background(), subagentStart("child", t.TempDir(), "RESPOND: "+long))
	e.as(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(child.ID, 1)
	res, err := e.svc.Notify.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Message.To, " "); got != lead.Address() {
		t.Fatalf("completion To = %q, want the parent only", got)
	}
	dir, err := e.mgr.Store.Dir(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "parent-output", "turn-1.txt")
	saved, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(saved), long) {
		t.Fatalf("saved completion: %v", err)
	}
	if strings.Contains(res.Message.Text, "THE-END") || !strings.Contains(res.Message.Text, "Full report: "+path) || len([]rune(res.Message.Text)) > parentoutput.InlineCharLimit+200 {
		t.Fatal("the stored completion must keep its short preview and pointer")
	}
	if state, err := e.mgr.Store.Info(lead.ID); err != nil || state.Turns != 1 || state.Status != subagent.StatusIdle {
		t.Fatalf("completion woke parent: %+v, %v", state, err)
	}

	feed, err := e.svc.FeedTail("alpha", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var row *Post
	for i := range feed.Posts {
		if feed.Posts[i].ID == res.Message.ID {
			row = &feed.Posts[i]
		}
	}
	if row == nil || !row.FullResult || row.ResultCut || row.Unread || !strings.HasSuffix(row.Text, long) {
		t.Fatalf("feed row = %+v", row)
	}
	if !strings.HasPrefix(row.Text, "alpha:child turn 1 completed. Result:\n\n") {
		t.Fatalf("feed row header = %q", row.Text[:60])
	}
	// The profile card offers the same full result and UI link as the feed.
	p, err := e.svc.Profile(child.ID, 3)
	if err != nil || len(p.Posts) != 1 || !p.Posts[0].FullResult || p.Posts[0].Detail == nil || p.Posts[0].Detail.ID != child.ID {
		t.Fatalf("profile posts = %+v, %v", p.Posts, err)
	}
}

func TestFullCompletionText(t *testing.T) {
	if got, cut := FullCompletionText("a:b", 2, "failed", nil, "boom\n"); got != "a:b turn 2 failed. Error: boom" || cut {
		t.Fatalf("error text = %q", got)
	}
	got, _ := FullCompletionText("a:b", 1, "completed", map[string]any{"n": 1}, "")
	if got != "a:b turn 1 completed. Result:\n\n```json\n{\n  \"n\": 1\n}\n```" {
		t.Fatalf("json text = %q", got)
	}
	big := "```\n" + strings.Repeat("é", MaxFeedResultKB*1024)
	got, cut := FullCompletionText("a:b", 1, "completed", big, "")
	if !cut || !strings.Contains(got, "Cut at 64 KB") || strings.Count(got, "```")%2 != 0 || strings.Contains(got, "dyna subagent output") {
		t.Fatalf("cut text tail = %q", got[len(got)-120:])
	}
}

// The cache keeps a bounded set of recent full results instead of every
// completion the engine ever served.
func TestResultCacheEvictsOldest(t *testing.T) {
	cache := newResultCache()
	for i := 0; i < resultCacheEntries+1; i++ {
		id := fmt.Sprintf("msg_%016x", i)
		cache.Add(id, fullResult{text: id})
	}
	if got := cache.Len(); got != resultCacheEntries {
		t.Fatalf("cache holds %d entries, want at most %d", got, resultCacheEntries)
	}
	if _, ok := cache.Get(fmt.Sprintf("msg_%016x", 0)); ok {
		t.Fatal("the oldest result was not evicted")
	}
	newest := fmt.Sprintf("msg_%016x", resultCacheEntries)
	if got, ok := cache.Get(newest); !ok || got.text != newest {
		t.Fatalf("newest result = %+v, %v; want it retained", got, ok)
	}
}

func subagentStart(name, dir, task string) subagent.StartOptions {
	return subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: name, Dir: dir, Task: task}
}

func TestRunCompletionShowsNameAndResult(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	writeRun(t, runstore.Meta{ID: "wf_20261016-000009-eeeeeeee", Name: "review", Status: "ok", RoomID: rec.ID, StartedAt: time.Now()}, nil, `{"confirmed":3}`)
	if _, err := e.svc.Notify.CompleteRun(e.ctx, "wf_20261016-000009-eeeeeeee"); err != nil {
		t.Fatal(err)
	}
	feed, _ := e.svc.FeedTail("alpha", "", 0)
	p := feed.Posts[len(feed.Posts)-1]
	if p.Speaker != "workflow review" || !p.FullResult || !strings.Contains(p.Text, "\"confirmed\":3") || p.Detail == nil || p.Detail.Kind != "run" || p.Detail.ID != "wf_20261016-000009-eeeeeeee" {
		t.Fatalf("run completion = speaker %q, text %q", p.Speaker, p.Text)
	}
	// A script that returns nothing has no result block, not "null".
	writeRun(t, runstore.Meta{ID: "wf_20261016-000010-ffffffff", Name: "sweep", Status: "ok", RoomID: rec.ID, StartedAt: time.Now()}, nil, "null")
	if _, err := e.svc.Notify.CompleteRun(e.ctx, "wf_20261016-000010-ffffffff"); err != nil {
		t.Fatal(err)
	}
	feed, _ = e.svc.FeedTail("alpha", "", 0)
	if p := feed.Posts[len(feed.Posts)-1]; p.Text != "No result." {
		t.Fatalf("empty run completion = %q", p.Text)
	}
}

func TestShellCompletionLinksToLogAcrossFeedAndPins(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	job, err := terminalsvc.ShellStore(e.svc.sessions().Root).Create(shells.StartOptions{Cmd: []string{"make", "test"}, Name: "test-build", Cwd: t.TempDir(), RoomID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Notify.CompleteShell(e.ctx, notify.ShellExit{ID: job.ID, RoomID: rec.ID, Description: "test-build", Code: 2, Tail: "FAIL TestBuild\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message.Text, "dyna shell tail "+job.ID) {
		t.Fatal("agent-facing notification lost its command pointer")
	}
	if err := e.svc.Pin(rec.ID, res.Message.ID, ""); err != nil {
		t.Fatal(err)
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	pins, err := e.svc.Pins(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Posts) != 1 || len(pins) != 1 {
		t.Fatalf("feed = %d, pins = %d", len(feed.Posts), len(pins))
	}
	for _, p := range []Post{feed.Posts[0], pins[0]} {
		if p.Detail == nil || p.Detail.Kind != "shell" || p.Detail.ID != job.ID || strings.Contains(p.Text, "dyna shell tail") || strings.Contains(p.Text, "test-build") || !strings.Contains(p.Text, "exit code 2") || !strings.Contains(p.Text, "FAIL TestBuild") || p.Speaker != "test-build" {
			t.Fatalf("shell UI completion = %+v", p)
		}
	}
	// A missing or mismatched record must not turn another room's job into a link.
	for _, roomID := range []string{"missing-room", ""} {
		p := Post{Message: res.Message, Completion: true}
		p.RoomID = roomID
		if got := e.svc.expandShell(p); got.Detail != nil || got.Text != res.Message.Text {
			t.Fatalf("mismatched completion = %+v", got)
		}
	}
}

func TestRunCancellationFeedKeepsRealErrors(t *testing.T) {
	for _, tc := range []struct{ name, status, errText, want string }{
		{"owner cancel", "canceled", "context canceled", "Canceled"},
		{"cancel without error", "canceled", "", "Canceled"},
		{"real failure", "error", "provider unavailable", "Error: provider unavailable"},
		{"failure mentioning cancellation", "error", "context canceled", "Error: context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("results")
			const id = "wf_20261017-000001-aaaaaaaa"
			writeRun(t, runstore.Meta{ID: id, Name: "review", Status: tc.status, Error: tc.errText, RoomID: rec.ID, Room: rec.Name, StartedAt: time.Now()}, nil, "")
			result, err := e.svc.Notify.CompleteRun(e.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == "canceled" {
				if ev, ok := result.Message.RunResult(); !ok || ev.State != room.RunStopped || strings.Contains(result.Message.Text, "Error:") || !strings.Contains(result.Message.Text, "Canceled") {
					t.Fatalf("stored cancel post: %+v", result.Message)
				}
			} else if ev, ok := result.Message.RunResult(); !ok || ev.State != room.RunFailed || !strings.Contains(ev.Summary, strings.TrimPrefix(tc.want, "Error: ")) {
				t.Fatalf("stored failure post: %+v", result.Message)
			}
			feed, err := e.svc.FeedTail(rec.ID, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(feed.Posts) != 1 || feed.Posts[0].Text != tc.want {
				t.Fatalf("feed: %+v", feed.Posts)
			}
		})
	}
}
