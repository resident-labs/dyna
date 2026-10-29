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

// A watched issue reports a new comment and a close once each. An edit to a
// comment the cursor already passed is not news.
func TestIssueWatchReportsOnlyNewFacts(t *testing.T) {
	w := issuewatch.Watch{Room: "room", Host: "github.com", Repo: "owner/repo", Number: 3, State: "open",
		Updated: "2026-10-29T10:00:00Z", CommentAt: "2026-10-29T10:00:00Z", CommentID: 5, EventID: 40}
	issue := issueObject{Title: "Crash", State: "closed", Updated: "2026-10-29T12:00:00Z"}
	comments := []Comment{
		{ID: 5, Body: "edited", Updated: "2026-10-29T11:00:00Z"},
		{ID: 6, Body: "new", Updated: "2026-10-29T11:30:00Z", User: User{Login: "alice"}},
	}
	log := []issueEvent{{ID: 40, Event: "labeled"}, {ID: 41, Event: "closed"}}
	events, next := nextIssueEvents(w, issue, comments, log)
	if len(events) != 2 {
		t.Fatalf("got %d events, want a comment and a close", len(events))
	}
	if events[0].Type != room.IssueWatchComment || events[0].Key != "6" || events[0].Commenter != "alice" {
		t.Fatalf("first event %+v, want comment 6 from alice", events[0])
	}
	if events[1].Type != room.IssueWatchClosed || events[1].Key != "41" {
		t.Fatalf("second event %+v, want closed as event 41", events[1])
	}
	if next.CommentID != 6 || next.EventID != 41 || next.CommentAt != "2026-10-29T11:30:00Z" || next.State != "closed" || next.Updated != issue.Updated {
		t.Fatalf("cursor %+v did not advance past comment 6 and event 41", next)
	}
}

// A close and a reopen between two polls both reach the room. The issue is
// open again at the poll, so comparing states alone would report nothing.
func TestIssueWatchKeepsCloseAndReopenBetweenPolls(t *testing.T) {
	w := issuewatch.Watch{Room: "room", Host: "github.com", Repo: "owner/repo", Number: 3, State: "open",
		Updated: "2026-10-29T10:00:00Z", EventID: 40}
	issue := issueObject{Title: "Crash", State: "open", Updated: "2026-10-29T12:00:00Z"}
	log := []issueEvent{{ID: 41, Event: "closed"}, {ID: 42, Event: "reopened"}}
	events, next := nextIssueEvents(w, issue, nil, log)
	if len(events) != 2 {
		t.Fatalf("got %d events, want a close and a reopen", len(events))
	}
	if events[0].Type != room.IssueWatchClosed || events[0].Key != "41" {
		t.Fatalf("first event %+v, want close as event 41", events[0])
	}
	if events[1].Type != room.IssueWatchReopened || events[1].Key != "42" {
		t.Fatalf("second event %+v, want reopen as event 42", events[1])
	}
	if next.EventID != 42 || next.State != "open" {
		t.Fatalf("cursor %+v did not advance to the reopen at event 42", next)
	}
}
