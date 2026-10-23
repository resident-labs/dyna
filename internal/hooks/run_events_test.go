package hooks

import (
	"context"
	"os/exec"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/room"
)

// A hook failure in a repo with no room creates the repo's room and posts its
// result there. A retried failure does not post again.
func TestFailedHookCreatesTheRepoRoom(t *testing.T) {
	s, repo := setup(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	s.Post = nil
	// A real exit status is what makes the run a failed hook, not an error.
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		return exitErr
	}
	fail := hook("fail", AfterLand)
	add(t, s, repo, fail)
	if _, err := s.Run(ctx, Event{Repo: repo}, fail.Name); err == nil {
		t.Fatal("the failing hook passed")
	}
	rooms, err := room.DefaultStore().List(true)
	if err != nil || len(rooms) != 1 || rooms[0].Repo == "" {
		t.Fatalf("rooms = %+v, %v", rooms, err)
	}
	posts, err := room.DefaultStore().Messages(rooms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// The start came earlier, when no room existed, so the failure alone posted.
	if len(posts) != 1 || posts[0].Kind != string(room.FamilyHook) || posts[0].Notice.Kind != string(room.TypeResult) {
		t.Fatalf("posts = %+v", posts)
	}
	run, ok := posts[0].Hook()
	if !ok || run.State != room.HookFailed || run.Hook != "fail" || run.RunID == "" || run.Detail != "exit 1" {
		t.Fatalf("stored result = %+v, %v", run, ok)
	}
	if _, err := s.Run(ctx, Event{Repo: repo}, fail.Name); err == nil {
		t.Fatal("the second failing hook passed")
	}
	// The retried run ends its own card too: two runs, two results. The first
	// run has no start, because the repo had no room when it began.
	posts, err = room.DefaultStore().Messages(rooms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]int{}
	starts := map[string]int{}
	for _, post := range posts {
		run, ok := post.Hook()
		if !ok {
			t.Fatalf("post %s is not a hook event", post.ID)
		}
		if post.Notice.Kind == string(room.TypeResult) {
			results[run.RunID]++
		} else {
			starts[run.RunID]++
		}
	}
	if len(results) != 2 {
		t.Fatalf("results = %v, want one per run", results)
	}
	for runID := range starts {
		if results[runID] != 1 {
			t.Fatalf("run %s started but did not end: %+v", runID, posts)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("starts = %v, want only the run inside the room to start", starts)
	}
}

// A hook that fails without an alert still ends its card, or the feed would
// show it running forever.
func TestFailedBlockHookEndsItsCard(t *testing.T) {
	s, repo := setup(t)
	block := hook("verify", BeforeLand)
	block.OnFail = "block"
	add(t, s, repo, block)
	var got []room.HookEvent
	s.Post = func(_ Event, ev room.HookEvent) error { got = append(got, ev); return nil }
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			return processguard.Execute(ctx, p)
		}
		return context.DeadlineExceeded
	}
	if _, err := s.run(t.Context(), Event{Repo: repo}, block.Name, nil, false); err == nil {
		t.Fatal("the block hook passed")
	}
	if len(got) != 2 || got[0].State != room.HookRunning || got[1].State != room.HookError {
		t.Fatalf("events = %+v", got)
	}
	if got[1].RunID != got[0].RunID || got[1].Detail != "timeout" {
		t.Fatalf("result = %+v", got[1])
	}
}

// A hook that passes in a repo that is nobody's room stays quiet: a routine
// run never creates a room.
func TestPassedHookPostsNoRoomForANewRepo(t *testing.T) {
	s, repo := setup(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	s.Post = nil
	s.Exec = passThroughGit
	pass := hook("pass", AfterLand)
	add(t, s, repo, pass)
	run, err := s.Run(ctx, Event{Repo: repo}, pass.Name)
	if err != nil || run.Result != "pass" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	rooms, err := room.DefaultStore().List(true)
	if err != nil || len(rooms) != 0 {
		t.Fatalf("a passing hook created a room: %+v, %v", rooms, err)
	}
}

// passThroughGit runs git for real and reports success for the hook command.
func passThroughGit(ctx context.Context, p processguard.Process) error {
	if p.Args[0] == "git" {
		return processguard.Execute(ctx, p)
	}
	return nil
}

// A room that already holds the repo gets the start and the result of one run,
// and the two events carry the same run ID so the web folds them into a card.
func TestHookRunPostsStartAndResultToTheRepoRoom(t *testing.T) {
	s, repo := setup(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	s.Post = nil
	s.Exec = passThroughGit
	rooms := room.DefaultStore()
	rec, err := rooms.EnsureRepoRoom(repo, "test")
	if err != nil {
		t.Fatal(err)
	}
	pass := hook("verify", AfterLand)
	pass.Label = "Verify the tree"
	add(t, s, repo, pass)
	run, err := s.Run(ctx, Event{Repo: repo}, pass.Name)
	if err != nil || run.Result != "pass" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	if posts[0].Notice.Kind != string(room.TypeStarted) || posts[1].Notice.Kind != string(room.TypeResult) {
		t.Fatalf("types = %s, %s", posts[0].Notice.Kind, posts[1].Notice.Kind)
	}
	start, _ := posts[0].Hook()
	result, _ := posts[1].Hook()
	if start.State != room.HookRunning || result.State != room.HookPassed || start.RunID != run.ID || result.RunID != run.ID {
		t.Fatalf("payloads = %+v / %+v", start, result)
	}
	if start.Label != "Verify the tree" || start.Command != "true" || start.Event != AfterLand {
		t.Fatalf("start payload = %+v", start)
	}
	for _, post := range posts {
		if post.From != room.EngineSender || post.Wake || post.Deliverable() {
			t.Fatalf("quiet engine post = %+v", post)
		}
	}
}
