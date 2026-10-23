package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

// steerEnv returns an env whose reviewer is assigned and mid-turn.
func steerEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	e := newEnv(t)
	e.setCI("success", 1)
	e.write()
	e.track()
	if err := e.s.Configure(e.author.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	id := e.record().Reviewer
	if id == "" || len(e.launches) != 1 {
		t.Fatalf("no reviewer: %+v", e.record())
	}
	info := e.reviewers[id]
	info.Status = subagent.StatusRunning
	e.reviewers[id] = info
	return e, id
}

func sentTo(e *testEnv, id string) []string {
	out := []string{}
	for _, n := range e.sent {
		if n.Target == id {
			out = append(out, n.Text)
		}
	}
	return out
}

// New discussion reaches the live reviewer once, without repeating its own comments.
func TestCommentsSteerLiveReviewer(t *testing.T) {
	e, id := steerEnv(t)
	before := len(sentTo(e, id))
	e.addTrustedComment(41, "issue-comments", "Owner: please keep the old flag.", "owner")
	e.f.Comments = append(e.f.Comments, Comment{ID: 42, Kind: "comments", Body: "Bug: nil map at parser.go:9", User: User{Login: "coderabbitai[bot]", Type: "Bot"}, Commit: e.f.Pull.Head.SHA, Path: "parser.go", Line: 9})
	e.addTrustedComment(43, "issue-comments", "Reviewer note to self", "owner")
	e.addTrustedComment(44, "issue-comments", "Author: fixed in the next push.", "owner")
	if err := e.s.Store.update(func(st *State) error {
		claimComment(st, "issue-comments", 43, id)
		claimComment(st, "issue-comments", 44, e.author.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// GitHub bumps updated_at for a new comment; the cheap poll relies on it.
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	got := sentTo(e, id)
	if len(got) != before+1 {
		t.Fatalf("comment steers: %d, want one batch", len(got)-before)
	}
	text := got[len(got)-1]
	for _, want := range []string{"keep the old flag", "nil map", "fixed in the next push", "Answer each comment in your final review"} {
		if !strings.Contains(text, want) {
			t.Errorf("steer misses %q: %s", want, text)
		}
	}
	if strings.Contains(text, "note to self") {
		t.Fatalf("reviewer got its own comment: %s", text)
	}
}

// The assignment context holds every PR comment the reviewer must answer,
// not only bot inline comments on the head.
func TestReviewContextHoldsAllComments(t *testing.T) {
	e, id := steerEnv(t)
	e.addTrustedComment(51, "issue-comments", "Owner question about naming", "owner")
	e.f.Comments = append(e.f.Comments, Comment{ID: 52, Kind: "comments", Body: "Old-head bot finding", User: User{Login: "coderabbitai[bot]", Type: "Bot"}, Commit: "old", Path: "a.go", Line: 1})
	e.addTrustedComment(53, "reviews", "My earlier review", "owner")
	if err := e.s.Store.update(func(st *State) error {
		claimComment(st, "reviews", 53, id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.SHA = strings.Repeat("c", 40)
	e.tick()
	paths, _ := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
	var input ReviewContext
	found := false
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var candidate ReviewContext
		if json.Unmarshal(data, &candidate) == nil && candidate.Assignment.Head == e.f.Pull.Head.SHA {
			input, found = candidate, true
		}
	}
	if !found {
		t.Fatalf("no context for the new head in %v", paths)
	}
	ids := map[int64]bool{}
	for _, c := range input.Comments {
		ids[c.ID] = true
	}
	if !ids[51] || !ids[52] || ids[53] {
		t.Fatalf("context comments: %+v", input.Comments)
	}
}

// authorFeedOnly turns review off, so a test of the author's comment feed
// sees no reviewer steering notices.
func (e *testEnv) authorFeedOnly() {
	e.t.Helper()
	if err := e.s.SetReviewEnabled(e.author.RoomID, false); err != nil {
		e.t.Fatal(err)
	}
}
