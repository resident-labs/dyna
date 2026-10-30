package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	accountservice "dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/updaterservice"
)

func TestQASeedFixtures(t *testing.T) {
	dir := t.TempDir()
	if err := qaSeedFixtures(dir); err != nil {
		t.Fatal(err)
	}
	usage := accountservice.CachedUsage(&accountstore.Store{Root: filepath.Join(dir, "accounts")}, time.Now())
	if len(usage.Accounts) != 6 {
		t.Fatalf("usage accounts = %d, want six", len(usage.Accounts))
	}
	for _, account := range usage.Accounts {
		if !account.Active || len(account.Windows) == 0 || account.Windows[0].UsedPercent == nil {
			t.Fatalf("usage fixture is not active with a reading: %+v", account)
		}
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
	history, err := (&watch.Service{Store: &watch.Store{Root: state.Root(dir)}}).RoomMergeHistory(rec.ID)
	if err != nil || len(history) != 1 || history[0].Outcome != "published" || len(history[0].Changes) != 1 {
		t.Fatalf("merge history = %v, %v", history, err)
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
