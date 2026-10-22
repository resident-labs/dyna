package subagent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A stray symlink in the sessions folder is skipped, as it always was: it
// must not hide the activity of real sessions.
func TestActiveRoomIDsSkipsAStraySymlink(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0 RESPOND: ok")
	e.wait(info.ID, 1)
	lock, err := os.OpenFile(filepath.Join(e.m.Store.root(), info.ID, "turn.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := os.Symlink(t.TempDir(), filepath.Join(e.m.Store.root(), "ses_stray")); err != nil {
		t.Fatal(err)
	}
	if live := e.m.Store.ActiveRoomIDs(); !live[info.RoomID] {
		t.Fatalf("ActiveRoomIDs = %v; want room %s of the session holding its turn lock", live, info.RoomID)
	}
}
