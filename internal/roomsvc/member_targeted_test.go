package roomsvc

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
)

func seedTargetedMembers(t *testing.T) (*env, room.Record, []subagent.Info, *memberWatch) {
	t.Helper()
	e := newEnv(t)
	rec := e.room("keyed")
	for i, id := range []string{"ses_20261021-120000-00000001", "ses_20261021-120000-00000002"} {
		recordtest.Write(t, e.mgr.Store.Root, id, subagent.Meta{
			ID: id, Name: []string{"a", "b"}[i], RoomID: rec.ID, Room: rec.Name,
			Depth: 1, Profile: "mock", Created: time.Unix(int64(2-i), 0),
		})
	}
	w := newMemberWatch()
	sessions, err := w.list(e.svc)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("baseline sessions = %+v, %v", sessions, err)
	}
	e.svc.scanMembers(sessions, []room.Record{rec}, w, nil, true, func(Event) {})
	return e, rec, sessions, w
}

func TestMemberRefreshReturnsOnlyDirtySession(t *testing.T) {
	e, rec, sessions, w := seedTargetedMembers(t)
	a, b := sessions[0], sessions[1]
	oldB := w.versions[b.ID]
	if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) { meta.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{a.ID: true}
	fresh, err := w.refresh(e.svc, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].ID != a.ID || fresh[0].Profile != "changed" {
		t.Fatalf("keyed scan input = %+v; want only updated %s", fresh, a.ID)
	}
	var events []Event
	collect := func(event Event) { events = append(events, event) }
	e.svc.scanMembers(fresh, []room.Record{rec}, w, ids, false, collect)
	if len(w.sessions) != 2 || len(w.versions) != 2 || w.versions[b.ID] != oldB {
		t.Fatalf("targeted scan lost the unrelated session: sessions %+v, versions %+v", w.sessions, w.versions)
	}
	if len(w.rooms[rec.ID].items) != 3 {
		t.Fatalf("targeted scan lost the full roster: %+v", w.rooms[rec.ID].items)
	}
	if len(events) != 2 {
		t.Fatalf("targeted changes = %+v; want one member and one session upsert", events)
	}
	for _, event := range events {
		if event.Change == nil || event.Change.ID != a.ID || event.Change.Op != "upsert" {
			t.Fatalf("targeted scan changed an unrelated member: %+v", event)
		}
	}
	events = nil
	fresh, err = w.refresh(e.svc, ids)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.scanMembers(fresh, []room.Record{rec}, w, ids, false, collect)
	if len(events) != 0 {
		t.Fatalf("unchanged targeted scan repeated %+v", events)
	}
}

// Follow the committed record event, with its session ID, through the same
// SessionChanged entry point that engine.publishState calls. The scan hook
// counts its actual input, not the size of the dirty set.
func TestCommittedSessionWriteScansOneMember(t *testing.T) {
	e, _, sessions, w := seedTargetedMembers(t)
	a := sessions[0]
	db, err := database.Open(filepath.Dir(e.mgr.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	var published []hub.Event
	state.For(db).SetPublish(func(event hub.Event) {
		if event.Topic == sessionlife.Topic {
			published = append(published, event)
			e.svc.SessionChanged(event.Key)
		}
	})
	var scanned []int
	e.svc.onMemberScan = func(n int) { scanned = append(scanned, n) }
	if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) { meta.Profile = "committed" }); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].Key != a.ID {
		t.Fatalf("committed session events = %+v; want one keyed event for %s", published, a.ID)
	}
	var members []Event
	e.svc.scanDirtyMembers(w, func(event Event) {
		if event.Kind == "members" {
			members = append(members, event)
		}
	})
	if len(scanned) != 1 || scanned[0] != 1 {
		t.Fatalf("one committed write scanned %v members; want [1]", scanned)
	}
	if len(members) != 1 || members[0].Change.ID != a.ID || members[0].Change.Op != "upsert" {
		t.Fatalf("committed write emitted %+v; want only %s", members, a.ID)
	}
}

func TestTargetedMemberScanPreservesOtherMembersOnMoveOrDelete(t *testing.T) {
	for _, remove := range []bool{false, true} {
		name := "move"
		if remove {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			e, rec, sessions, w := seedTargetedMembers(t)
			a, b := sessions[0], sessions[1]
			oldB := w.versions[b.ID]
			var other room.Record
			if remove {
				recordtest.Remove(t, e.mgr.Store.Root, a.ID)
			} else {
				other = e.room("destination")
				if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) {
					meta.RoomID, meta.Room = other.ID, other.Name
				}); err != nil {
					t.Fatal(err)
				}
			}
			e.svc.SessionChanged(a.ID)
			var events []Event
			e.svc.scanDirtyMembers(w, func(event Event) { events = append(events, event) })
			if w.versions[b.ID] != oldB || len(w.rooms[rec.ID].items) != 2 || w.rooms[rec.ID].items[0].Children != 1 {
				t.Fatalf("targeted %s lost the other member or owner count: versions %+v, roster %+v", name, w.versions, w.rooms[rec.ID].items)
			}
			deleted, moved := false, false
			for _, event := range events {
				if event.Change.ID == b.ID {
					t.Fatalf("targeted %s emitted unrelated change %+v", name, event)
				}
				if event.Kind == "members" && event.Change.ID == a.ID {
					deleted = deleted || event.RoomID == rec.ID && event.Change.Op == "delete"
					moved = moved || event.RoomID == other.ID && event.Change.Op == "upsert"
				}
			}
			if !deleted {
				t.Fatalf("targeted %s omitted old-room deletion: %+v", name, events)
			}
			if remove {
				if _, exists := w.versions[a.ID]; exists || len(w.sessions) != 1 {
					t.Fatalf("deleted session remains cached: %+v, %+v", w.versions, w.sessions)
				}
			} else if !moved || len(w.rooms[other.ID].items) != 2 || w.rooms[other.ID].items[0].Children != 1 {
				t.Fatalf("move lost destination roster or owner count: events %+v, roster %+v", events, w.rooms[other.ID].items)
			}
		})
	}
}
