package goals

import (
	"errors"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestGoalChangesUseSharedWriterWithoutDuplicateEvents(t *testing.T) {
	s := testStore(t)
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	w := state.For(db)
	var events []hub.Event
	w.SetPublish(func(ev hub.Event) { events = append(events, ev) })
	t.Cleanup(func() { w.SetPublish(nil) })
	before := w.Health().Writes
	g, err := s.Add("r", "owner", Input{Text: "Ship the writer", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish("r", g.ID, "owner", "Shipped", false); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("abort the goal change")
	if err := s.Update(func(st *State) error {
		st.Room("r").Goals[g.ID].Text = "Must not commit"
		return failed
	}); !errors.Is(err, failed) {
		t.Fatalf("failed update returned %v", err)
	}
	if got := w.Health().Writes - before; got != 3 {
		t.Fatalf("shared writer recorded %d writes; want 3", got)
	}
	for _, read := range []func() (State, error){s.Read, s.ReadLocked, s.ReadOpen} {
		st, err := read()
		if err != nil {
			t.Fatal(err)
		}
		if stored := st.Room("r").Goals[g.ID]; stored != nil && stored.Text != g.Text {
			t.Fatalf("failed update changed goal to %+v", stored)
		}
	}
	if _, err := s.OpenHeldBy("r", "owner"); err != nil {
		t.Fatal(err)
	}
	if got := w.Health().Writes - before; got != 5 {
		t.Fatalf("shared writer recorded %d transactions; want 5 including two lifecycle barriers", got)
	}
	// roomsvc.Watch still publishes the goals topic until its own move.
	if len(events) != 0 {
		t.Fatalf("goal writes published duplicate events: %+v", events)
	}
}
