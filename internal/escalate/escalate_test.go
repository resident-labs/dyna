package escalate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

const room = "room_20261018-000000-aaaaaaaa"

func id(n int) string { return "ses_20261018-00000" + string(rune('0'+n)) + "-aaaaaaaa" }

type fixture struct {
	t      *testing.T
	m      *subagent.Manager
	starts []subagent.StartOptions
	fail   map[string]bool // profiles whose start fails
	seen   int             // starts before the last route call
}

func newFixture(t *testing.T) *fixture {
	root := storetest.Isolate(t)
	f := &fixture{t: t, m: &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}}, fail: map[string]bool{}}
	dataDir := filepath.Dir(f.m.Store.Root)
	t.Cleanup(func() { database.Close(dataDir) })
	if err := state.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	f.m.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{{Name: "author-profile"}, {Name: "off", Disabled: true}}}, nil
	}
	return f
}

// session writes a session record. parent is 0 for a top-level session.
func (f *fixture) session(t *testing.T, n, parent int, edit func(*subagent.Meta)) {
	t.Helper()
	m := subagent.Meta{ID: id(n), Name: "s" + string(rune('0'+n)), RoomID: room, Room: "r", Profile: "author-profile"}
	if parent > 0 {
		m.ParentSession = id(parent)
	}
	if edit != nil {
		edit(&m)
	}
	recordtest.Write(t, f.m.Store.Root, m.ID, m)
}

func (f *fixture) start(o subagent.StartOptions) (subagent.Info, error) {
	f.starts = append(f.starts, o)
	if f.fail[o.Profile] {
		return subagent.Info{}, errors.New("start failed")
	}
	f.session(f.t, 9, 0, func(m *subagent.Meta) { m.Profile = o.Profile })
	return f.m.Store.Info(id(9))
}

// route returns the chosen session and whether it is not the owner.
func (f *fixture) route(t *testing.T, r Request) (subagent.Info, bool) {
	t.Helper()
	r.Room = room
	if r.Key == "" {
		r.Key = "pr:owner/repo#1@abc"
	}
	if r.Brief == "" {
		r.Brief = "Fix CI on PR #1."
	}
	info, spawned, err := Route(context.Background(), f.m, f.start, r)
	if err != nil {
		t.Fatal(err)
	}
	if spawned != (len(f.starts) > f.seen && info.ID != "") {
		t.Fatalf("spawned=%v after %d starts", spawned, len(f.starts))
	}
	f.seen = len(f.starts)
	return info, info.ID != ""
}

func closed(m *subagent.Meta)   { m.Closed = time.Now() }
func finished(m *subagent.Meta) { m.Finished = time.Now(); m.Status = subagent.StatusDormant }

func TestChainSkipsClosedSessions(t *testing.T) {
	f := newFixture(t)
	f.session(t, 5, 0, nil)
	f.session(t, 2, 5, closed)
	f.session(t, 1, 2, closed)
	info, ok := f.route(t, Request{Subject: id(1)})
	if !ok || info.ID != id(5) || len(f.starts) != 0 {
		t.Fatalf("got %s ok=%v starts=%d, want the open top-level session", info.ID, ok, len(f.starts))
	}
}

// A message wakes a dormant session, so escalation routes to it.
func TestChainRoutesToDormantSession(t *testing.T) {
	f := newFixture(t)
	f.session(t, 5, 0, nil)
	f.session(t, 4, 5, finished)
	f.session(t, 1, 4, closed)
	info, ok := f.route(t, Request{Subject: id(1)})
	if !ok || info.ID != id(4) || len(f.starts) != 0 {
		t.Fatalf("got %s ok=%v starts=%d, want the dormant parent", info.ID, ok, len(f.starts))
	}
}

func TestIdleSubjectIsChosen(t *testing.T) {
	f := newFixture(t)
	f.session(t, 2, 0, nil)
	f.session(t, 1, 2, nil)
	if info, ok := f.route(t, Request{Subject: id(1)}); !ok || info.ID != id(1) || info.Status != subagent.StatusIdle {
		t.Fatalf("got %s %s ok=%v, want the idle subject", info.ID, info.Status, ok)
	}
	if info, ok := f.route(t, Request{Subject: id(1), Skip: []string{id(1)}}); !ok || info.ID != id(2) {
		t.Fatalf("skip: got %s ok=%v, want the parent", info.ID, ok)
	}
}

func TestSpawnHappensOncePerKey(t *testing.T) {
	f := newFixture(t)
	f.session(t, 1, 0, closed)
	for range 2 {
		info, ok := f.route(t, Request{Subject: id(1), Brief: "Land or close PR #1."})
		if !ok || info.ID != id(9) {
			t.Fatalf("got %s ok=%v, want the spawned agent", info.ID, ok)
		}
	}
	// Once the spawned agent closes, the record still blocks a second spawn.
	f.session(t, 9, 0, closed)
	if _, ok := f.route(t, Request{Subject: id(1)}); ok {
		t.Fatal("closed spawned agent was chosen")
	}
	if len(f.starts) != 1 || f.starts[0].Profile != "author-profile" || f.starts[0].Room != room || f.starts[0].Task != "Land or close PR #1." {
		t.Fatalf("starts: %+v", f.starts)
	}
	if _, ok := f.route(t, Request{Subject: id(1), Key: "pr:owner/repo#1@def"}); !ok || len(f.starts) != 2 {
		t.Fatalf("a new key did not spawn: starts=%d", len(f.starts))
	}
}

func TestUnusableProfileFallsBackToTheDefault(t *testing.T) {
	f := newFixture(t)
	f.session(t, 1, 0, func(m *subagent.Meta) { closed(m); m.Profile = "off" })
	if _, ok := f.route(t, Request{Subject: id(1)}); !ok || len(f.starts) != 1 || f.starts[0].Profile != "" {
		t.Fatalf("disabled profile: %+v", f.starts)
	}
	f = newFixture(t)
	f.session(t, 1, 0, closed)
	f.fail["author-profile"] = true
	if _, ok := f.route(t, Request{Subject: id(1)}); !ok || len(f.starts) != 2 || f.starts[1].Profile != "" {
		t.Fatalf("failed start: %+v", f.starts)
	}
}

func TestEmptyBriefNeverStartsAnAgent(t *testing.T) {
	f := newFixture(t)
	f.session(t, 1, 0, closed)
	info, spawned, err := Route(context.Background(), f.m, f.start, Request{Room: room, Subject: id(1), Key: "k"})
	if err != nil || spawned || info.ID != "" || len(f.starts) != 0 {
		t.Fatalf("got %s spawned=%v starts=%d err=%v, want the owner and no start", info.ID, spawned, len(f.starts), err)
	}
}

func TestOwnerIsTheLastResortWhenSpawnFails(t *testing.T) {
	f := newFixture(t)
	f.session(t, 1, 0, closed)
	f.fail["author-profile"], f.fail[""] = true, true
	if info, ok := f.route(t, Request{Subject: id(1)}); ok || info.ID != "" {
		t.Fatalf("got %s ok=%v, want the owner", info.ID, ok)
	}
	if info, ok := f.route(t, Request{}); ok || info.ID != "" {
		t.Fatalf("empty subject: got %s ok=%v, want the owner", info.ID, ok)
	}
}

func TestSQLiteRecordKeepsClosedSessionDedupeKey(t *testing.T) {
	f := newFixture(t)
	req := Request{Key: "once"}
	f.route(t, req)
	f.session(t, 9, 0, closed)
	if _, ok := f.route(t, req); ok || len(f.starts) != 1 {
		t.Fatalf("closed session released key: starts=%d", len(f.starts))
	}
	db, err := database.Open(filepath.Dir(f.m.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	var session string
	if err := db.QueryRow("SELECT session FROM escalation_records WHERE key = ?", req.Key).Scan(&session); err != nil || session != id(9) {
		t.Fatalf("stored session = %q, %v", session, err)
	}
}
