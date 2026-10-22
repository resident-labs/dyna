package reposcope

import (
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Scope and limit writes go through the one state writer: each counts once
// in its Health and publishes nothing.
func TestScopeWritesGoThroughTheStateWriter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	w := state.For(s.db)
	var mu sync.Mutex
	var events []hub.Event
	w.SetPublish(func(e hub.Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	before := w.Health().Writes
	if err := s.Set("acct", []string{"github.com/org/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLimited("acct", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := w.Health().Writes - before; got != 2 {
		t.Fatalf("Set and MarkLimited made %d state writes, want 2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("scope writes published %v; no consumer reads them", events)
	}
}
