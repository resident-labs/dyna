package subagent

import (
	"os"
	"testing"
	"time"

	"dyna-agent/internal/goals"
)

// A long-closed session stays while it still matters: a goal it set or an
// open child. Otherwise maintenance may remove it.
func TestKeepClosedSession(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	room, _ := e.start("SLEEP:0 RESPOND: ok")
	room, _ = e.wait(room.ID, 1)
	write := func(m Meta) {
		dir, _ := e.m.Store.Dir(m.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := e.m.Store.writeMeta(m); err != nil {
			t.Fatal(err)
		}
	}
	closed := time.Now().Add(-40 * 24 * time.Hour)
	plain := Meta{ID: NewID(), Name: "plain", RoomID: room.RoomID, Room: room.Room, Profile: "mock", Closed: closed}
	setter := Meta{ID: NewID(), Name: "setter", RoomID: room.RoomID, Room: room.Room, Profile: "mock", Closed: closed}
	parent := Meta{ID: NewID(), Name: "parent", RoomID: room.RoomID, Room: room.Room, Profile: "mock", Closed: closed}
	child := Meta{ID: NewID(), Name: "child", RoomID: room.RoomID, Room: room.Room, Profile: "mock", ParentSession: parent.ID, Depth: 2}
	open := setter
	open.Closed = time.Time{}
	write(open)
	if _, err := e.m.goalStore().Add(room.RoomID, setter.ID, goals.Input{Text: "Still wanted"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Meta{plain, setter, parent, child} {
		write(m)
	}
	for _, tc := range []struct {
		id   string
		keep bool
	}{{plain.ID, false}, {setter.ID, true}, {parent.ID, true}} {
		keep, err := e.m.KeepClosedSession(e.ctx, tc.id)
		if err != nil || keep != tc.keep {
			t.Errorf("KeepClosedSession(%s) = %v, %v; want %v", tc.id, keep, err, tc.keep)
		}
	}
}

// A consolidation source is closed, but its target still reads its record
// and turn files for delivery. Retention keeps the source while the target's
// record names it, however old the source is.
func TestRetainClosedSessionKeepsConsolidationSources(t *testing.T) {
	e, now := consolidationEnv(t)
	e.m.Store.Now = func() time.Time { return *now }
	a, _ := e.start("SLEEP:1 RESPOND: target")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: source")
	b, _ = e.wait(b.ID, 1)
	plain, _ := e.start("SLEEP:1 RESPOND: plain")
	plain, _ = e.wait(plain.ID, 1)
	c := consolidatePair(t, e, a, b)
	submitAll(t, e, c)
	finishConsolidation(t, e, c)
	source, err := e.m.Store.ReadMeta(b.ID)
	if err != nil || source.Closed.IsZero() || source.ConsumedBy != a.ID {
		t.Fatalf("source after consolidation = %+v, %v; want closed and consumed by %s", source, err, a.ID)
	}
	e.doneGoal(plain)
	if _, err := e.m.CloseWithOptions(e.ctx, plain.ID, CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	for _, id := range []string{b.ID, plain.ID} {
		if _, err := e.m.Store.UpdateMeta(id, func(m *Meta) { m.Closed = old }); err != nil {
			t.Fatal(err)
		}
	}
	retain, err := e.m.RetainClosedSession(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kept, err := retain(e.ctx, b.ID); err != nil || !kept {
		t.Fatalf("retain(consolidation source) = %v, %v; want kept", kept, err)
	}
	if kept, err := retain(e.ctx, plain.ID); err != nil || kept {
		t.Fatalf("retain(plain closed session) = %v, %v; want removable", kept, err)
	}
	target, err := e.m.Store.ReadMeta(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.pendingRoomMessages(target); err != nil {
		t.Fatalf("the target cannot read its inherited inbox: %v", err)
	}
}
