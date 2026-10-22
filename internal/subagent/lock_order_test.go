package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/lockrank"
	"dyna-agent/internal/room"
)

// The documented order is session before room. Taking a session lock inside a
// room snapshot inverts it, which can deadlock silently, so tests panic.
func TestSessionLockInsideRoomSnapshotPanics(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	var recovered any
	err := e.m.rooms().WithRoomSnapshot(e.ctx, info.RoomID, func(room.Record) error {
		defer func() { recovered = recover() }()
		return e.m.Store.withLock(info.ID, func() error { return nil })
	})
	if msg, _ := recovered.(string); err != nil || !strings.Contains(msg, "session lock taken while holding a room lock") {
		t.Fatalf("recovered=%v err=%v", recovered, err)
	}
}

// The names hook ranks below sessions, and the gate hook ranks above them.
func TestSessionLockHooksKeepTheirRanks(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	for name, inverted := range map[string]func() error{
		"names lock taken while holding a session lock": func() error {
			return e.m.Store.withLock(info.ID, func() error {
				unlock, err := lockNames(e.m.Store)
				if err == nil {
					unlock()
				}
				return err
			})
		},
		"session lock taken while holding a gate lock": func() error {
			return e.m.Store.withGateLock(info.ID, func(string) error {
				lockrank.Acquire(lockrank.Session)()
				return nil
			})
		},
	} {
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_ = inverted()
		}()
		if msg, _ := recovered.(string); !strings.Contains(msg, name) {
			t.Errorf("want %q, recovered %v", name, recovered)
		}
	}
}
