package subagent

import (
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"testing"
)

func TestCheckInDefersHeldTurnAndDeliversAfterRelease(t *testing.T) {
	s, id := turnStore(t)
	m := &Manager{Store: s}
	rec, err := m.rooms().Create(room.CreateInput{Name: "checkin"})
	if err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, s.Root, id, Meta{ID: id, RoomID: rec.ID, Room: rec.Name, Status: StatusIdle, Turns: 1})
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnQueued}); err != nil {
		t.Fatal(err)
	}
	lock, err := s.tryTurnLock(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	writes := 0
	write := func() (string, error) { writes++; return "msg_0123456789abcdef", nil }
	for _, status := range []string{TurnQueued, TurnRunning} {
		if err := s.writeTurn(id, Turn{N: 1, Status: status}); err != nil {
			t.Fatal(err)
		}
		done, err := m.DeliverCheckIn(t.Context(), id, write)
		if err != nil || done || writes != 0 {
			t.Fatalf("%s: delivered=%v writes=%d err=%v", status, done, writes, err)
		}
	}
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	if done, err := m.DeliverCheckIn(t.Context(), id, write); err != nil || done || writes != 0 {
		t.Fatalf("held completed turn: %v %v %d", done, err, writes)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if done, err := m.DeliverCheckIn(t.Context(), id, write); err != nil || !done || writes != 1 {
		t.Fatalf("released turn: %v %v %d", done, err, writes)
	}
}
