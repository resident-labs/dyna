package roomsvc

import (
	"maps"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/listchange"
)

func TestGoalScanSkipsUnchangedRoomsAndPreservesDeletes(t *testing.T) {
	e := newEnv(t)
	first, other := e.room("changed"), e.room("unchanged")
	store := e.svc.goalService().Store
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	g, err := store.Add(first.ID, "owner", goals.Input{Text: "Finish", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	u, err := store.Add(other.ID, "owner", goals.Input{Text: "Keep", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	type scanner struct {
		rows, work map[string]string
		version    string
		events     []Event
	}
	scans := []*scanner{{rows: map[string]string{}, work: map[string]string{}}, {rows: map[string]string{}, work: map[string]string{}}}
	scan := func(s *scanner, first bool) {
		e.svc.scanGoalsAt(now, s.rows, s.work, &s.version, first, func(ev Event) { s.events = append(s.events, ev) })
	}
	for _, s := range scans {
		scan(s, true)
		if len(s.events) != 0 {
			t.Fatal("initial scan emitted events", s.events)
		}
	}
	if _, err := store.Finish(first.ID, g.ID, "owner", "Done", false); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(store.Root))
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err := db.QueryRow("SELECT body FROM goals WHERE room=? AND id=?", other.ID, u.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goals SET body='{"id":12}' WHERE room=? AND id=?`, other.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	key := listchange.Key("goals", other.ID, u.ID)
	for _, s := range scans {
		before := s.rows[key]
		scan(s, false)
		if len(s.events) != 1 || s.events[0].RoomID != first.ID || s.events[0].Change == nil || s.events[0].Change.Op != "upsert" || s.events[0].Change.ID != g.ID || !s.events[0].WorkerWake {
			t.Fatalf("changed room must publish without reading the unrelated body: %+v", s.events)
		}
		if before == "" || s.rows[key] != before {
			t.Fatal("unrelated snapshot changed")
		}
		s.events = nil
	}
	if _, err := db.Exec("UPDATE goals SET body=? WHERE room=? AND id=?", body, other.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *goals.State) error { delete(st.Room(first.ID).Goals, g.ID); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, s := range scans {
		scan(s, false)
		if len(s.events) != 1 || s.events[0].RoomID != first.ID || s.events[0].Change == nil || s.events[0].Change.Op != "delete" || s.events[0].Change.ID != g.ID || !s.events[0].WorkerWake {
			t.Fatalf("expected one deletion: %+v", s.events)
		}
		s.events = nil
		scan(s, false)
		if len(s.events) != 0 || s.rows[key] == "" {
			t.Fatalf("unchanged scan emitted events or lost unrelated snapshot: %+v", s.events)
		}
	}
	// A failed read must not consume revisions or change either scan cache.
	if _, err := store.Edit(other.ID, u.ID, "owner", "Keep going", nil, true); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT body FROM goals WHERE room=? AND id=?", other.ID, u.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goals SET body='{"id":12}' WHERE room=? AND id=?`, other.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range scans {
		rows, work, version := maps.Clone(s.rows), maps.Clone(s.work), s.version
		scan(s, false)
		if len(s.events) != 0 || !maps.Equal(s.rows, rows) || !maps.Equal(s.work, work) || s.version != version {
			t.Fatal("failed scan advanced its snapshots")
		}
	}
	if _, err := db.Exec("UPDATE goals SET body=? WHERE room=? AND id=?", body, other.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range scans {
		scan(s, false)
		if len(s.events) != 1 || s.events[0].RoomID != other.ID || s.events[0].Change == nil || s.events[0].Change.ID != u.ID {
			t.Fatalf("retry lost the changed goal: %+v", s.events)
		}
		s.events = nil
	}
	if _, err := store.AddRule(goals.Global, "owner", "owner", "Keep every room current"); err != nil {
		t.Fatal(err)
	}
	for _, s := range scans {
		scan(s, false)
		if len(s.events) != 2 || s.events[0].Change != nil || s.events[1].Change != nil {
			t.Fatalf("global rules must update both rooms: %+v", s.events)
		}
		s.events = nil
		scan(s, false)
		if len(s.events) != 0 {
			t.Fatal("global rule change repeated")
		}
	}
	scoped, err := store.ReadRooms([]string{first.ID})
	if err != nil || len(scoped.Rooms) != 1 || scoped.Rooms[first.ID] == nil || scoped.Global == nil || len(scoped.Global.Rules) != 1 {
		t.Fatalf("scoped read lost global rules or included unrelated rooms: %+v, %v", scoped, err)
	}

}
