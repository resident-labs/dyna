package roomsvc

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

type retiredRunnerProcess struct{ exit chan bool }

func (p retiredRunnerProcess) wait(*quitter) bool { return <-p.exit }
func (retiredRunnerProcess) close()               {}

func TestMemberTickDoesNotImportOldRunnerTurn(t *testing.T) {
	e := newEnv(t)
	rec := e.room("runner-retirement")
	const id = "ses_20261023-070000-00000001"
	recordtest.Write(t, e.mgr.Store.Root, id, subagent.Meta{ID: id, Turns: 1, Room: rec.Name, RoomID: rec.ID})
	path := filepath.Join(e.mgr.Store.Root, id, "turns", "1.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"n":1,"status":"ok","prompt":"imported"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Dir(e.mgr.Store.Root)
	if err := subagent.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	readRow := func() (string, int64) {
		t.Helper()
		var raw string
		var revision int64
		if err := db.QueryRow("SELECT turn, revision FROM session_turns WHERE session=? AND n=1", id).Scan(&raw, &revision); err != nil {
			t.Fatal(err)
		}
		return raw, revision
	}
	wake := make(chan struct{}, 1)
	w := newMemberWatch()
	w.runners = newRunnerWatch(func() { wake <- struct{}{} }, func(p callerid.Process) (callerid.Launched, bool, error) {
		if p.PID != os.Getpid() {
			t.Fatalf("recorded process = %+v; want the test process", p)
		}
		return callerid.Launched{Identity: callerid.Identity{Kind: callerid.Session, Session: id}, Format: 0}, true, nil
	})
	handle := retiredRunnerProcess{exit: make(chan bool, 1)}
	w.runners.open = func(int) (runnerProcess, error) { return handle, nil }
	t.Cleanup(func() { close(handle.exit); w.runners.close() })
	info := runningInfo(id, os.Getpid())
	info.Turns = 1
	w.runners.follow([]subagent.Info{info})
	if w.runners.needsFullScans() || len(wake) != 0 {
		t.Fatal("a verified old-format runner fell back to full scans")
	}
	if !e.svc.membersChanged(w, nil, true) || e.svc.membersChanged(w, nil, false) {
		t.Fatal("member ticks did not establish an idle baseline")
	}
	raw, rowRevision := readRow()
	revision := w.guard.revision
	if err := os.WriteFile(path, []byte(`{"n":1,"status":"ok","prompt":"stale file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e.svc.membersChanged(w, nil, false) {
		t.Error("a retained turn file made the member tick request a scan")
	}
	if got, gotRevision := readRow(); got != raw || gotRevision != rowRevision {
		t.Errorf("member tick changed the SQL turn: %s at %d; want %s at %d", got, gotRevision, raw, rowRevision)
	}
	if got, err := e.mgr.Store.MembersRevision(); err != nil || got != revision {
		t.Errorf("members revision = %q, %v; want unchanged %q", got, err, revision)
	}
	if _, err := e.mgr.Store.UpdateMeta(id, func(m *subagent.Meta) { m.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	if !e.svc.membersChanged(w, nil, false) {
		t.Fatal("a canonical revision change did not request a member scan")
	}
	handle.exit <- true
	<-wake
}
