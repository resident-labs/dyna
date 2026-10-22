package librarianstate

import (
	"errors"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestUpdateUsesSharedWriter(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(s.DataDir) })
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) { t.Errorf("unexpected watched topic: %+v", event) })
	before := writer.Health().Writes
	if _, err := s.Update("repo", true, func(st *State) error { st.Session = "librarian"; return nil }); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("reject update")
	if _, err := s.Update("repo", false, func(st *State) error { st.Session = "unsaved"; return rejected }); !errors.Is(err, rejected) {
		t.Fatalf("rejected update = %v", err)
	}
	got, err := s.Get("repo")
	if err != nil || got.Session != "librarian" {
		t.Fatalf("state = %+v, error = %v", got, err)
	}
	if n := writer.Health().Writes - before; n != 2 {
		t.Fatalf("shared writer counted %d writes; want 2", n)
	}
}
