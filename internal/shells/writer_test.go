package shells

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Shell job writes go through the one state writer. Each counts once in its
// Health and publishes nothing, because the room watch still scans shell
// jobs (item 16). exec still reports whether its row matched, so exactly one
// ClaimExit wins.
func TestShellWritesGoThroughTheStateWriter(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	w := state.For(db)
	var mu sync.Mutex
	var events []hub.Event
	w.SetPublish(func(e hub.Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	t.Cleanup(func() { w.SetPublish(func(hub.Event) {}) })
	before := w.Health().Writes
	if err := s.Save(meta, &Exit{Code: 3, Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := w.Health().Writes - before; got != 1 {
		t.Fatalf("Save made %d state writes, want 1", got)
	}
	first, err := s.ClaimExit(meta.ID)
	if err != nil || !first {
		t.Fatalf("first ClaimExit = %v, %v; want true", first, err)
	}
	again, err := s.ClaimExit(meta.ID)
	if err != nil || again {
		t.Fatalf("second ClaimExit = %v, %v; want false", again, err)
	}
	if got := w.Health().Writes - before; got != 3 {
		t.Fatalf("Save and two ClaimExit calls made %d state writes, want 3", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("shell writes published %v; the room watch owns shell events", events)
	}
}
