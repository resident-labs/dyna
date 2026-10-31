package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
