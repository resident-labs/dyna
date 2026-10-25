package issue

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

// TestIssueReferenceShowsTheIssue stops the D-61 mistake: a lone issue
// reference reads that issue instead of filing an empty one titled after it,
// and a whole title that is only an issue number is refused.
func TestIssueReferenceShowsTheIssue(t *testing.T) {
	f := newIssueFixture(t)
	filed := f.file(t)
	for _, args := range [][]string{
		{filed.Label()},
		{strings.ToLower(filed.Label())},
		{strings.Replace(filed.Label(), "D-", "d", 1)},
		{filed.ID},
		{"show", filed.Label()},
	} {
		out, err := f.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		for _, want := range []string{filed.Label(), "Fix parser", "details"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v printed %q, want %q", args, out, want)
			}
		}
	}
	if rows, err := f.rooms.Issues(f.rec.ID, true); err != nil || len(rows) != 1 {
		t.Fatalf("a reference filed an issue: %+v, %v", rows, err)
	}
	if _, err := f.rooms.FileIssue(f.rec.ID, room.IssueInput{Title: "D-9", Filer: f.rec.Name}); err == nil || !strings.Contains(err.Error(), "dyna issue show D-9") {
		t.Fatalf("a bare issue number became a title: %v", err)
	}
	// A title that only begins like a post ID is a title, with its details.
	if out, err := f.run("msg_id lookup fails in the feed", "--note", "seen twice"); err != nil || !strings.Contains(out, "filed D-2") {
		t.Fatalf("a title that starts with msg_ was read as an ID: %q, %v", out, err)
	}
	if out, err := f.run("show", "D-2"); err != nil || !strings.Contains(out, "msg_id lookup fails in the feed") || !strings.Contains(out, "seen twice") {
		t.Fatalf("the filed title lost its details: %q, %v", out, err)
	}
	// An issue filed without a note shows no note, and never the filing summary.
	if _, err := f.run("No note here"); err != nil {
		t.Fatal(err)
	}
	out, err := f.run("D-3")
	if err != nil || strings.Contains(out, "filed") || strings.Count(out, "\n") != 2 {
		t.Fatalf("an issue without a note shows %q, %v", out, err)
	}
}

// TestIssueShowKeepsLiveState gives a single issue the same goal and PR state
// the list shows, so a show of active work does not read as untouched.
func TestIssueShowKeepsLiveState(t *testing.T) {
	f := newIssueFixture(t)
	filed := f.file(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	goal, err := f.svc.AddGoal(ctx, f.rec.ID, "Ship the parser", []string{"@room"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.rooms.LinkIssueGoal(ctx, f.rec.ID, filed.ID, goal.ID, f.rec.Name, room.IssueStateOpen); err != nil {
		t.Fatal(err)
	}
	raw, err := f.run(filed.Label(), "--json")
	if err != nil {
		t.Fatal(err)
	}
	var shown shownIssue
	if err := json.Unmarshal([]byte(raw), &shown); err != nil || !shown.InProgress || shown.GoalID != goal.ID {
		t.Fatalf("show lost the live state: %s %v", raw, err)
	}
	raw, err = f.run("list", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []room.Issue
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 || !rows[0].InProgress || rows[0].GoalID != goal.ID {
		t.Fatalf("list lost the live state: %s %v", raw, err)
	}
}
