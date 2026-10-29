package watch

import (
	"testing"

	"dyna-agent/internal/pr/issuewatch"
	"dyna-agent/internal/room"
)

// The tick stays due while an issue is watched, even with no PR watched. The
// watch's room is missing, so its poll fails before any GitHub read; the
// watched count still reports true.
func TestIssueWatchKeepsTickDueWithoutPRWatch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &Service{}
	dir := s.dataDir()
	if err := issuewatch.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	watched, err := s.pollIssueWatches(t.Context())
	if err != nil || watched {
		t.Fatalf("no watch: watched=%v err=%v, want false and nil", watched, err)
	}
	if err := (issuewatch.Store{DataDir: dir}).Save(t.Context(), issuewatch.Watch{Room: "missing", Host: "github.com", Repo: "owner/repo", Number: 3}); err != nil {
		t.Fatal(err)
	}
	watched, err = s.pollIssueWatches(t.Context())
	if err == nil || !watched {
		t.Fatalf("watched issue: watched=%v err=%v, want true and an error", watched, err)
	}
}

// A watched issue reports a new comment and a state change once each. An edit
// to a comment the cursor already passed is not news.
func TestIssueWatchReportsOnlyNewFacts(t *testing.T) {
	w := issuewatch.Watch{Room: "room", Host: "github.com", Repo: "owner/repo", Number: 3, State: "open",
		Updated: "2026-10-29T10:00:00Z", CommentAt: "2026-10-29T10:00:00Z", CommentID: 5}
	issue := issueObject{Title: "Crash", State: "closed", Updated: "2026-10-29T12:00:00Z"}
	comments := []Comment{
		{ID: 5, Body: "edited", Updated: "2026-10-29T11:00:00Z"},
		{ID: 6, Body: "new", Updated: "2026-10-29T11:30:00Z", User: User{Login: "alice"}},
	}
	events, next := nextIssueEvents(w, issue, comments)
	if len(events) != 2 {
		t.Fatalf("got %d events, want a comment and a close", len(events))
	}
	if events[0].Type != room.IssueWatchComment || events[0].Key != "6" || events[0].Commenter != "alice" {
		t.Fatalf("first event %+v, want comment 6 from alice", events[0])
	}
	if events[1].Type != room.IssueWatchClosed || events[1].Key != issue.Updated {
		t.Fatalf("second event %+v, want closed at %s", events[1], issue.Updated)
	}
	if next.CommentID != 6 || next.CommentAt != "2026-10-29T11:30:00Z" || next.State != "closed" || next.Updated != issue.Updated {
		t.Fatalf("cursor %+v did not advance past comment 6 and the close", next)
	}
}
