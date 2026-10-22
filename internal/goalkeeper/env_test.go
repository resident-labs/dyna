package goalkeeper

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

const (
	setterID = "ses_20261018-000001-aaaaaaaa"
	parentID = "ses_20261018-000002-bbbbbbbb"
	holderID = "ses_20261018-000003-cccccccc"
	agentID  = "ses_20261018-000009-eeeeeeee"
)

// nopSpawner accepts a woken turn without running it.
type nopSpawner struct{}

func (nopSpawner) Spawn(_ *subagent.Manager, _ string, _ int, lock *os.File) error {
	return lock.Close()
}

// escalationEnv is a room with real room and session stores.
type escalationEnv struct {
	t     *testing.T
	s     *Service
	rooms *room.Store
	rec   room.Record
	mgr   *subagent.Manager
	goal  goals.Goal
	n     int
}

// newEscalationEnv makes a goal set by setterID (a child of parentID) and held
// by holderID. All three sessions start open.
func newEscalationEnv(t *testing.T) *escalationEnv {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	e := &escalationEnv{t: t, rooms: &room.Store{Root: filepath.Join(root, "rooms")}}
	var err error
	if e.rec, err = e.rooms.Create(room.CreateInput{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	e.mgr = &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}, Rooms: e.rooms, Spawner: nopSpawner{}, Settings: settings.Defaults,
		Profiles: func() (*profile.Store, error) {
			return &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}}, nil
		}}
	e.s = New()
	e.s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
	e.s.Notify = notify.New(e.rooms, e.mgr)
	e.session(parentID, "", true)
	e.session(setterID, parentID, true)
	e.session(holderID, setterID, true)
	if e.goal, err = e.s.Store.Add(e.rec.ID, setterID, goals.Input{Text: "Ship it", Holders: []string{holderID}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *escalationEnv) session(id, parent string, open bool) {
	e.t.Helper()
	meta := subagent.Meta{ID: id, Name: id[len(id)-8:], Room: e.rec.Name, RoomID: e.rec.ID, ParentSession: parent, Profile: "mock", Resumable: true, HarnessSession: "h-" + id}
	if !open {
		meta.Closed = time.Now()
	}
	recordtest.Write(e.t, e.mgr.Store.Root, id, meta)
	if err := os.MkdirAll(filepath.Join(e.mgr.Store.Root, id, "turns"), 0700); err != nil {
		e.t.Fatal(err)
	}
}
