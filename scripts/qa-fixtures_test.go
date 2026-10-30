package main

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/updaterservice"
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
	install := hookinstall.Store{Root: filepath.Join(dir, "hooks", "install")}
	runs, err := install.Recent(updaterservice.RunName)
	if err != nil || len(runs) != 1 || runs[0].Status != "finished" || runs[0].Result != "pass" {
		t.Fatalf("update runs = %v, %v", runs, err)
	}
	if log, err := os.ReadFile(runs[0].HookLog); err != nil || string(log) != "Sample update completed.\n" {
		t.Fatalf("update log = %q, %v", log, err)
	}
	if err := qaSeedFixtures(dir); err == nil {
		t.Fatal("accepted a nonempty directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "dyna.db")); err != nil {
		t.Fatal(err)
	}
}
