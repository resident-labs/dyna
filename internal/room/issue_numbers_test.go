package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestConcurrentIssueNumbersAreUnique(t *testing.T) {
	s, rec, _ := pinFixture(t)
	const count = 8
	results := make(chan Issue, count)
	errors := make(chan error, count)
	for range count {
		go func() {
			i, err := s.FileIssue(rec.ID, "Concurrent", "", "", 0, rec.Name)
			results <- i
			errors <- err
		}()
	}
	seen := map[int]bool{}
	for range count {
		i := <-results
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if i.Number < 1 || i.Number > count || seen[i.Number] {
			t.Fatalf("invalid or reused number: %+v", i)
		}
		seen[i.Number] = true
	}
}

func TestIssueNumbersUpgradeUsesCreationOrderOnce(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Root: filepath.Join(dir, "rooms")}
	const roomID = "room_20261022-120000-1234abcd"
	if err := os.MkdirAll(filepath.Join(s.Root, roomID), 0700); err != nil {
		t.Fatal(err)
	}
	// Schema 2 already imported JSON. An obsolete, damaged source must not
	// prevent numbering the authoritative SQLite rows.
	if err := os.WriteFile(filepath.Join(s.Root, roomID, IssuesFile), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(issuesSchema + `INSERT INTO meta(key,value) VALUES('room.issues.schema','2');`); err != nil {
		t.Fatal(err)
	}
	for _, i := range []Issue{
		{ID: "newer", FiledAt: 200, State: "closed", Reason: "done"},
		{ID: "older", FiledAt: 100, State: "open"},
	} {
		body, _ := json.Marshal(i)
		if _, err := db.Exec(`INSERT INTO room_issues(room_id,id,state,body) VALUES(?,?,?,?)`, roomID, i.ID, i.State, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		rows, err := s.Issues(roomID, true)
		if err != nil || len(rows) != 2 || rows[0].ID != "older" || rows[0].Number != 1 || rows[1].Number != 2 {
			t.Fatalf("backfill: %+v %v", rows, err)
		}
	}
}

func TestIssueReferenceNumbersReachPostsAndAgentFrames(t *testing.T) {
	s, rec, _ := pinFixture(t)
	i, err := s.FileIssue(rec.ID, "Remember me", "", "", 0, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	refs := []Reference{{Kind: "room_issue", Number: i.ID, Title: i.Title, State: "open"}}
	legacy := Message{ID: newMessageID(), RoomID: rec.ID, From: rec.Name, To: []string{rec.Name}, Kind: "message", References: refs}
	line, _ := json.Marshal(legacy)
	path, err := s.messagesPath(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	original, _ := os.ReadFile(path)
	stored, err := s.Message(rec.ID, legacy.ID)
	if err != nil || stored.References[0].Number != "D-1" {
		t.Fatalf("historical reference: %+v %v", stored, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(original) {
		t.Fatal("reading a numbered reference changed the room log")
	}
	posted, err := s.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, References: refs, Forward: NewForward(stored)})
	if err != nil || posted.References[0].Number != "D-1" {
		t.Fatalf("new reference: %+v %v", posted, err)
	}
	frame := posted.Frame(FrameOptions{Room: rec.Name})
	if strings.Count(frame, "Room issue D-1: Remember me") != 2 || strings.Contains(frame, "Room issue #msg_") {
		t.Fatalf("agent frame: %s", frame)
	}
}
