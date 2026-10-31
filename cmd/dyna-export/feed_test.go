package main

import (
	"database/sql"
	"os"

	"github.com/resident-labs/dyna/contract"
	"path/filepath"
	"testing"
)

func TestLegacyIssueFeedRequiresReviewForSelectedIssue(t *testing.T) {
	root := t.TempDir()
	id := "room_20260101-000000-aabbccdd"
	dir := filepath.Join(root, "rooms", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte("{\"id\":\"legacy\",\"kind\":\"issue\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	feed, err := readFeed(root, id)
	if err != nil || feed.legacyIssues != 1 {
		t.Fatalf("legacy feed = %+v, %v", feed, err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE room_issues(room_id TEXT,id TEXT,state TEXT,body TEXT,seq INTEGER);
CREATE TABLE room_issue_numbers(room_id TEXT,message_id TEXT,number INTEGER);
INSERT INTO room_issues VALUES ('r','legacy','open','{"id":"legacy"}',1);`)
	if err != nil {
		t.Fatal(err)
	}
	author := func(string) (contract.ImportedAuthor, error) { return contract.ImportedAuthor{}, nil }
	if _, err := readIssues(db, "r", feed, author); err == nil {
		t.Fatal("accepted a selected legacy issue")
	}
	if _, err := readIssues(db, "other", feed, author); err != nil {
		t.Fatalf("irrelevant legacy issue: %v", err)
	}
}

func TestFeedSkipsOnlyUnterminatedFinalLine(t *testing.T) {
	root := t.TempDir()
	id := "room_20260101-000000-aabbccdd"
	dir := filepath.Join(root, "rooms", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "messages.jsonl")
	if err := os.WriteFile(file, []byte("{\"from\":\"alice\",\"text\":\"hello\"}\n{\"from\":"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readFeed(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.partial || got.lastPosts["alice"] != "hello" {
		t.Fatalf("feed lost complete message or partial notice: %+v", got)
	}
	if err := os.WriteFile(file, []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFeed(root, id); err == nil {
		t.Fatal("accepted malformed complete line")
	}
}
