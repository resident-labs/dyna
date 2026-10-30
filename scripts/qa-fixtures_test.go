package main

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

func TestQASeedFixtures(t *testing.T) {
	dir := t.TempDir()
	if err := qaSeedFixtures(dir); err != nil {
		t.Fatal(err)
	}
	rooms := &room.Store{Root: filepath.Join(dir, "rooms")}
	rec, err := rooms.Resolve("qa-demo")
	if err != nil {
		t.Fatal(err)
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %v, %v", posts, err)
	}
	gs := &goals.Store{Root: filepath.Join(dir, "goals")}
	list, err := gs.List(rec.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("goals = %v, %v", list, err)
	}
	prs, err := state.Read(state.Root(dir))
	if err != nil || len(prs.Records) != 1 {
		t.Fatalf("PRs = %v, %v", prs.Records, err)
	}
	if err := qaSeedFixtures(dir); err == nil {
		t.Fatal("accepted a nonempty directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "dyna.db")); err != nil {
		t.Fatal(err)
	}
}
