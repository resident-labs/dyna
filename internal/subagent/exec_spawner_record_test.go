package subagent

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
)

// A turn runner acts for its session, so its launch is recorded for the
// engine's caller identity (requirements item 3).
func TestExecSpawnerRecordsTheTurn(t *testing.T) {
	data := t.TempDir()
	runner := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Store: &Store{Root: filepath.Join(data, "sessions")}, Executable: runner}
	id := "ses_20261020-000000-abcdefgh"
	if err := os.MkdirAll(filepath.Join(data, "sessions", id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sessionlife.Save(m.Store.Root, id, Meta{ID: id, Profile: "mock", RoomID: "rooms", Room: "rooms"}); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Create(filepath.Join(t.TempDir(), "turn.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := (ExecSpawner{}).Spawn(m, id, 1, lock); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	var actor, session, launcher string
	var pid int
	if err := db.QueryRow(`SELECT actor, session, launcher, pid FROM caller_records`).Scan(&actor, &session, &launcher, &pid); err != nil || actor != "session" || session != id || launcher != "turn" || pid == 0 {
		t.Fatalf("turn record = %s %q %q pid %d (%v), want session %s turn", actor, session, launcher, pid, err, id)
	}
}
