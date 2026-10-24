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
	_, err = store.AddWithIssues(rec.ID, "owner", rec.Name, Input{Text: "Work", Shared: true}, []int{issue.Number})
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
