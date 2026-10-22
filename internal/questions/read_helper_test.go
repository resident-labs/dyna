package questions

import (
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestQuestionLifecycleReadsUseSharedBarrier(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	if _, err := s.Ask(sample("owner"), nil); err != nil {
		t.Fatal(err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) { t.Errorf("read published %+v", event) })
	before := writer.Health().Writes
	if _, err := s.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List("room"); err != nil {
		t.Fatal(err)
	}
	if got := writer.Health().Writes; got != before {
		t.Fatalf("ordinary reads counted as writes: %d -> %d", before, got)
	}
	if _, err := s.ReadLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenAskedBy("room", "owner"); err != nil {
		t.Fatal(err)
	}
	if got := writer.Health().Writes - before; got != 2 {
		t.Fatalf("shared writer counted %d lifecycle barriers; want 2", got)
	}
}
