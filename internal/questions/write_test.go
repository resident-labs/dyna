package questions

import (
	"errors"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestQuestionChangesUseSharedWriter(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	writer := state.For(db)
	before := writer.Health().Writes
	writer.SetPublish(func(event hub.Event) {
		if event.Topic != "questions" {
			t.Errorf("unexpected event: %+v", event)
		}
	})
	if err := s.update(func(st *State) error { st.Next["room"]++; return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := s.Read()
	if err != nil || current.Next["room"] != 1 {
		t.Fatalf("committed state = %+v, %v", current, err)
	}
	refused := errors.New("refused")
	if err := s.update(func(st *State) error { st.Next["room"]++; return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if err := s.update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := writer.Health().Writes - before; got != 3 {
		t.Fatalf("shared writer counted %d writes; want 3", got)
	}
	current, err = s.Read()
	if err != nil || current.Next["room"] != 1 {
		t.Fatalf("state after rollback = %+v, %v", current, err)
	}
}
