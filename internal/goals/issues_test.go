package goals

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
)

func TestAddWithIssuesRollsBackCommittedLinkWhenEventFails(t *testing.T) {
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := rooms.FileIssue(rec.ID, room.IssueInput{Title: "Linked work", Filer: rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	var body string
	if err := db.QueryRow(`SELECT body FROM room_issues WHERE room_id=? AND id=?`, rec.ID, issue.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var stored room.Issue
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Title = ""
	bodyBytes, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE room_issues SET body=? WHERE room_id=? AND id=?`, bodyBytes, rec.ID, issue.ID); err != nil {
		t.Fatal(err)
	}

	store := &Store{Root: filepath.Join(root, "goals"), Issues: rooms}
	_, err = store.AddWithIssues(rec.ID, "owner", rec.Name, Input{Text: "Work", Shared: true}, []int{issue.Number}, ClaimAny)
	if err == nil || !strings.Contains(err.Error(), "event needs the field \"title\"") {
		t.Fatalf("error = %v, want event posting failure", err)
	}
	rows, err := rooms.Issues(rec.ID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("issues = %+v, %v", rows, err)
	}
	if rows[0].State != "open" || rows[0].GoalID != "" {
		t.Fatalf("committed link was not rolled back: %+v", rows[0])
	}
	all, err := store.List(rec.ID)
	if err != nil || len(all) != 0 {
		t.Fatalf("failed creation left goals: %+v, %v", all, err)
	}
}

// A second goal cannot claim an issue that another goal just took, and the
// losing goal is deleted: the claim reads the stored state in the step that
// writes the link. A caller that means to replace finished work still relinks.
func TestClaimOpenRefusesClaimedIssue(t *testing.T) {
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := rooms.FileIssue(rec.ID, room.IssueInput{Title: "One owner", Filer: rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(root) })
	store := &Store{Root: filepath.Join(root, "goals"), Issues: rooms}
	first, err := store.AddWithIssues(rec.ID, "owner", rec.Name, Input{Text: "First", Shared: true}, []int{issue.Number}, ClaimOpen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AddWithIssues(rec.ID, "owner", rec.Name, Input{Text: "Second", Shared: true}, []int{issue.Number}, ClaimOpen); err == nil {
		t.Fatal("a second goal claimed a claimed issue")
	}
	rows, err := rooms.Issues(rec.ID, true)
	if err != nil || len(rows) != 1 || rows[0].State != "goal" || rows[0].GoalID != first.ID {
		t.Fatalf("issue = %+v, %v, want its first goal %s", rows, err, first.ID)
	}
	all, err := store.List(rec.ID)
	if err != nil || len(all) != 1 || all[0].ID != first.ID {
		t.Fatalf("goals = %+v, %v, want only %s", all, err, first.ID)
	}
	replaced, err := store.AddWithIssues(rec.ID, "owner", rec.Name, Input{Text: "Third", Shared: true}, []int{issue.Number}, ClaimAny)
	if err != nil {
		t.Fatalf("deliberate replacement was refused: %v", err)
	}
	rows, err = rooms.Issues(rec.ID, true)
	if err != nil || len(rows) != 1 || rows[0].GoalID != replaced.ID {
		t.Fatalf("issue = %+v, %v, want the replacing goal %s", rows, err, replaced.ID)
	}
}
