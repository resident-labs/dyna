package room

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/lockrank"
)

// Each room lock hook registers its rank: a session lock (a lower rank) taken
// inside it panics.
func TestRoomLockHooksRankAboveSessions(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "lock-order"})
	if err != nil {
		t.Fatal(err)
	}
	for name, hold := range map[string]func(func() error) error{
		"room root": s.withRootLock,
		"room":      func(fn func() error) error { return s.withRoomLock(rec.ID, fn) },
		"room snapshot": func(fn func() error) error {
			return s.WithRoomSnapshot(context.Background(), rec.ID, func(Record) error { return fn() })
		},
	} {
		var recovered any
		_ = hold(func() error {
			defer func() { recovered = recover() }()
			lockrank.Acquire(lockrank.Session)()
			return nil
		})
		want := map[string]string{"room root": "room root", "room": "room", "room snapshot": "room"}[name]
		if msg, _ := recovered.(string); !strings.Contains(msg, "session lock taken while holding a "+want+" lock") {
			t.Errorf("%s hook: recovered %v", name, recovered)
		}
	}
}
