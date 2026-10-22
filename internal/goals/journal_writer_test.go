package goals

import (
	"sync"
	"testing"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestJournalUsesSharedAppendWithoutUnusedEvents(t *testing.T) {
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
	entry := JournalEntry{Round: "round-1", At: s.now()}
	first, err := s.AddJournal("r", entry)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.AddJournal("r", entry)
	if err != nil || retry.ID != first.ID {
		t.Fatalf("round retry = %+v, %v; want %s", retry, err, first.ID)
	}
	if got := w.Health().Writes - before; got != 1 {
		t.Fatalf("shared append recorded %d writes; want 1", got)
	}
	if len(events) != 0 {
		t.Fatalf("journal emitted events with no consumer: %+v", events)
	}
}

func TestConcurrentJournalRetriesAppendOnce(t *testing.T) {
	s := testStore(t)
	entry := JournalEntry{Round: "round-1", At: s.now()}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AddJournal("r", entry); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, _, err := s.Journal("r", "", 0)
	if err != nil || len(entries) != 1 || entries[0].ID != "j0" {
		t.Fatalf("journal = %+v, %v; want one round at j0", entries, err)
	}
}
