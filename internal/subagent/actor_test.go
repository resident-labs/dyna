package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

func clearStamp(t *testing.T) {
	t.Helper()
	storetest.ClearStamp(t)
}

func actorFixture(t *testing.T) (*Manager, *room.Store, room.Record, map[string]Meta) {
	t.Helper()
	clearStamp(t)
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	work, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Store: &Store{Root: filepath.Join(t.TempDir(), "sessions")}}
	metas := map[string]Meta{}
	for _, s := range []struct{ name, id, parent string }{
		{"lead", "ses_20261018-000001-aaaaaaaa", ""},
		{"child", "ses_20261018-000002-bbbbbbbb", "ses_20261018-000001-aaaaaaaa"},
		{"grandchild", "ses_20261018-000003-cccccccc", "ses_20261018-000002-bbbbbbbb"},
		{"other", "ses_20261018-000004-dddddddd", ""},
	} {
		if err := os.MkdirAll(filepath.Join(m.Store.Root, s.id), 0o700); err != nil {
			t.Fatal(err)
		}
		meta := Meta{ID: s.id, Name: s.name, RoomID: work.ID, Room: work.Name, ParentSession: s.parent, Profile: "mock"}
		if err := m.Store.writeMeta(meta); err != nil {
			t.Fatal(err)
		}
		metas[s.name] = meta
	}
	return m, rooms, work, metas
}

func stampAs(t *testing.T, meta Meta) {
	t.Helper()
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, meta.ID)
	t.Setenv(mode.EnvRoomID, meta.RoomID)
}

func TestActorIsStampedSessionOrRoot(t *testing.T) {
	m, rooms, work, metas := actorFixture(t)
	other, err := rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Actor(rooms, m.Store.Resolve, "work")
	if err != nil || got.ID != "" || got.RoomID != work.ID || got.Dir == "" {
		t.Fatalf("root actor = %+v, %v", got.Meta, err)
	}
	stampAs(t, metas["child"])
	got, err = Actor(rooms, m.Store.Resolve, "")
	if err != nil || got.ID != metas["child"].ID || got.RoomID != work.ID {
		t.Fatalf("stamped actor = %+v, %v", got.Meta, err)
	}
	if _, err = Actor(rooms, m.Store.Resolve, other.Name); err == nil {
		t.Fatal("stamped session acted in another room")
	}
}

func TestTargetLimitsStampedSessionToItselfAndDescendants(t *testing.T) {
	m, _, _, metas := actorFixture(t)
	for _, name := range []string{"lead", "child", "grandchild", "other"} {
		if _, err := m.Target(metas[name].ID); err != nil {
			t.Fatalf("root target %s: %v", name, err)
		}
	}
	stampAs(t, metas["child"])
	for _, name := range []string{"child", "grandchild"} {
		if got, err := m.Target(metas[name].ID); err != nil || got.ID != metas[name].ID {
			t.Fatalf("child target %s = %v, %v", name, got.ID, err)
		}
	}
	for _, name := range []string{"lead", "other"} {
		if _, err := m.Target(metas[name].ID); err == nil || !strings.Contains(err.Error(), "itself and its descendants") {
			t.Fatalf("child acted on %s: %v", name, err)
		}
	}
}

func TestStartDirDefaultsToParentSessionDir(t *testing.T) {
	m, _, work, metas := actorFixture(t)
	lead := metas["lead"]
	lead.Dir = t.TempDir()
	if err := m.Store.writeMeta(lead); err != nil {
		t.Fatal(err)
	}
	got, err := m.startDir(StartOptions{Cwd: t.TempDir()}, work, lead.ID)
	if err != nil || got != lead.Dir {
		t.Fatalf("startDir = %q, %v; want parent dir %q", got, err, lead.Dir)
	}
}

func TestStartDirOutsideRepoFailsUnlessExplicit(t *testing.T) {
	m, _, work, _ := actorFixture(t)
	work.Repo = "owner/repo"
	cwd := t.TempDir()
	if _, err := m.startDir(StartOptions{Cwd: cwd}, work, ""); err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Fatalf("implicit dir outside a work tree = %v; want an error naming --dir", err)
	}
	if got, err := m.startDir(StartOptions{Dir: cwd}, work, ""); err != nil || got != cwd {
		t.Fatalf("explicit dir = %q, %v; want %q", got, err, cwd)
	}
}
