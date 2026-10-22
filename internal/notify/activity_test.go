package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestActiveRoomIDsUsesOwningStores(t *testing.T) {
	root := storetest.DataDir(t)
	svc := New(&room.Store{Root: filepath.Join(root, "rooms")}, &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}})
	write := func(path string, v any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const id = "ses_20261017-120000-00000001"
	recordtest.Write(t, filepath.Join(root, "sessions"), id, subagent.Meta{ID: id, RoomID: "session", Room: "session"})
	lock, err := os.OpenFile(filepath.Join(root, "sessions", id, "turn.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if svc.ActiveRoomIDs()["session"] {
		t.Fatal("a shared status probe counted as a running turn")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Runs an earlier release wrote: only their meta.json files.
	write(filepath.Join(root, "runs", "wf_live", "meta.json"), runstore.Meta{ID: "wf_live", RoomID: "run", Status: "running", Pid: os.Getpid()})
	write(filepath.Join(root, "runs", "wf_stale", "meta.json"), runstore.Meta{ID: "wf_stale", RoomID: "stale-run", Status: "running", Pid: 999999999})
	const shell = "sh_20261017-120000-00000001"
	shellStore := &shells.Store{Root: filepath.Join(root, "shells")}
	job := shells.Meta{ID: shell, RoomID: "shell", Supervisor: os.Getpid(), Cmd: []string{"true"}, Started: time.Now()}
	if err := shellStore.Save(job, nil); err != nil {
		t.Fatal(err)
	}
	live := svc.ActiveRoomIDs()
	for _, id := range []string{"session", "run", "shell"} {
		if !live[id] {
			t.Errorf("missing activity for %s: %v", id, live)
		}
	}
	if live["stale-run"] {
		t.Fatal("dead workflow counted as active")
	}
	if err := shellStore.Save(job, &shells.Exit{Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if svc.ActiveRoomIDs()["shell"] {
		t.Fatal("finished shell counted as active")
	}
}

func TestActiveRoomIDsKeepsHeldTurnsWithoutRepair(t *testing.T) {
	root := t.TempDir()
	const id = "ses_20261017-120000-00000001"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, root, id, subagent.Meta{ID: id, RoomID: "pending", Room: "pending", Turns: 1})
	turn, _ := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnQueued, RoomHeld: true})
	if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), turn, 0600); err != nil {
		t.Fatal(err)
	}
	store := &subagent.Store{Root: root, ReportRepaired: func(*subagent.Store, string, int) { t.Error("activity query repaired a turn") }}
	if !store.ActiveRoomIDs()["pending"] {
		t.Fatal("durable held turn did not keep its room active")
	}
}

func TestListWithActivityPreservesBusyRooms(t *testing.T) {
	now := time.Now()
	store := &room.Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	rec, err := store.Create(room.CreateInput{Name: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	list, err := store.ListWithActivity(false, map[string]bool{rec.ID: true})
	if err != nil || len(list) != 1 {
		t.Fatalf("busy rooms = %+v, %v", list, err)
	}
	list, err = store.ListWithActivity(false, nil)
	if err != nil || len(list) != 0 {
		t.Fatalf("idle rooms = %+v, %v", list, err)
	}
}
