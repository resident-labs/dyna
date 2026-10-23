package room

import (
	"dyna-agent/internal/subagent"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/shells"
	datastore "dyna-agent/internal/store"
)

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func deleteFixture(t *testing.T) (*room.Store, room.Record, room.Record) {
	t.Helper()
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st := room.DefaultStore()
	old, err := st.Create(room.CreateInput{Name: "old-work"})
	if err != nil {
		t.Fatal(err)
	}
	keep, err := st.Create(room.CreateInput{Name: "keep-work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []room.Record{old, keep} {
		if _, err := st.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{rec.Name + ":lead"}, Text: "hello " + rec.Name}); err != nil {
			t.Fatal(err)
		}
	}
	return st, old, keep
}

func writeSession(t *testing.T, rec room.Record, name string, closed bool) {
	t.Helper()
	id := "ses_20261016-120000-" + strings.Repeat(string(name[0]), 8)
	m := map[string]any{"id": id, "name": name, "roomId": rec.ID, "room": rec.Name, "profile": "mock", "dir": "/tmp", "depth": 1, "created": time.Now(), "lastActivity": time.Now()}
	if closed {
		m["closed"] = time.Now()
	}
	recordtest.Write(t, runstore.SessionsDir(), id, m)
}

func TestRoomDeleteRemovesRoomAndLeavesOthers(t *testing.T) {
	st, old, keep := deleteFixture(t)
	writeSession(t, old, "done", true) // closed sessions do not block
	out, err := execCmd(t, RoomCmd(), "delete", "old-work")
	if err != nil || !strings.Contains(out, "deleted room old-work") {
		t.Fatalf("delete = %q, %v", out, err)
	}
	if _, err := st.Resolve(old.ID); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("room still resolves: %v", err)
	}
	if _, err := st.Messages(old.ID); err == nil {
		t.Fatal("log still readable")
	}
	list, err := execCmd(t, RoomCmd(), "list", "--all")
	if err != nil || strings.Contains(list, "old-work") || !strings.Contains(list, "keep-work") {
		t.Fatalf("list = %q, %v", list, err)
	}
	msgs, err := st.Messages(keep.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hello keep-work" {
		t.Fatalf("other room changed: %v %v", msgs, err)
	}
	// Delete by ID also works.
	if _, err := execCmd(t, RoomCmd(), "delete", keep.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRoomDeleteRefusesUnknown(t *testing.T) {
	deleteFixture(t)
	if _, err := execCmd(t, RoomCmd(), "delete", "no-such-room"); !errors.Is(err, room.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
}

func TestRoomDeleteRefusesOpenSessionAndLiveWork(t *testing.T) {
	st, old, _ := deleteFixture(t)
	writeSession(t, old, "idle", false)
	_, err := execCmd(t, RoomCmd(), "delete", "old-work")
	if err == nil || !strings.Contains(err.Error(), "old-work:idle (open)") {
		t.Fatalf("open session err = %v", err)
	}
	// Close it; a running run now blocks.
	writeSession(t, old, "idle", true)
	runstoretest.Write(t, datastore.DataDir(), runstore.Meta{ID: "wf_busy", Status: "running", Pid: os.Getpid(), RoomID: old.ID, StartedAt: time.Now()})
	_, err = execCmd(t, RoomCmd(), "delete", "old-work")
	if err == nil || !strings.Contains(err.Error(), "run wf_busy (running)") {
		t.Fatalf("run err = %v", err)
	}
	// A finished run does not block, but a live shell does.
	runstoretest.Write(t, datastore.DataDir(), runstore.Meta{ID: "wf_busy", Status: "ok", Pid: os.Getpid(), RoomID: old.ID, StartedAt: time.Now()})
	sh, err := shells.DefaultStore().Create(shells.StartOptions{Cmd: []string{"sleep", "1"}, RoomID: old.ID, Room: old.Name, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = execCmd(t, RoomCmd(), "delete", "old-work")
	if err == nil || !strings.Contains(err.Error(), "shell "+sh.ID) {
		t.Fatalf("shell err = %v", err)
	}
	if _, err := st.Resolve(old.ID); err != nil {
		t.Fatalf("refused delete removed the room: %v", err)
	}
	if msgs, err := st.Messages(old.ID); err != nil || len(msgs) != 1 {
		t.Fatalf("refused delete changed the log: %v %v", msgs, err)
	}
}

func TestRoomDeleteBlockedFromInsideDyna(t *testing.T) {
	deleteFixture(t)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", "ses_20261016-120000-abcdef12")
	t.Setenv("DYNA_ROOM_ID", "room_20261016-120000-abcdef12")
	t.Setenv("DYNA_ROOM", "keep-work")
	if _, err := execCmd(t, RoomCmd(), "delete", "old-work"); err == nil || !strings.Contains(err.Error(), "room delete is owner or root only;") {
		t.Fatalf("err = %v", err)
	}
}

func TestRoomDeleteDoesNotRepairSessionsUnderRoomLock(t *testing.T) {
	_, old, keep := deleteFixture(t)
	id := "ses_20261017-120000-abcdef12"
	recordtest.Write(t, runstore.SessionsDir(), id, subagent.Meta{
		ID: id, Name: "orphan", RoomID: keep.ID, Room: keep.Name, Turns: 1, Created: time.Now(),
	})
	writeJSONFile(t, filepath.Join(runstore.SessionsDir(), id, "turns", "1.json"), subagent.Turn{N: 1, Status: subagent.TurnRunning})
	if _, err := execCmd(t, RoomCmd(), "delete", old.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := subagent.DefaultStore().ReadTurn(id, 1)
	if err != nil || turn.Status != subagent.TurnRunning {
		t.Fatalf("deletion changed the orphaned turn: %+v, %v", turn, err)
	}
}

func TestRoomDeleteRefusesUnreadableSession(t *testing.T) {
	st, old, _ := deleteFixture(t)
	id := "ses_20261017-120000-abcdef12"
	recordtest.WriteRaw(t, runstore.SessionsDir(), id, []byte("{"))
	if _, err := execCmd(t, RoomCmd(), "delete", old.ID); err == nil || !strings.Contains(err.Error(), id+" (unreadable:") {
		t.Fatalf("delete past unreadable session = %v", err)
	}
	if _, err := st.Resolve(old.ID); err != nil {
		t.Fatalf("blocked delete removed room: %v", err)
	}
}
