package roomsvc

import (
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// The owner entry shows who holds the root seat, and the internal root is
// not counted as a top-level session. The app's "Take back seat" gives the
// seat back to the internal root.
func TestMembersShowTheSeatAndReturnSeatTakesItBack(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	e.start(rec, "lead", nil)
	if _, err := e.svc.Notify.Post(e.ctx, notify.PostInput{Sender: notify.Sender{Kind: notify.SenderOwner, Room: rec.Name, Human: true}, Mentions: []string{"@root"}, Text: "SLEEP:0\nRESPOND: hi"}); err != nil {
		t.Fatal(err)
	}
	root, ok, err := e.mgr.RootSession(rec.ID)
	if err != nil || !ok {
		t.Fatalf("no internal root: %v", err)
	}
	e.wait(root.ID, 1)
	if _, _, err := e.svc.Notify.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	members, err := e.svc.Members("alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	owner := members[0]
	if !owner.Owner || owner.Seat == nil || !owner.Seat.External || owner.Seat.Since.IsZero() || owner.Children != 1 {
		t.Fatalf("owner entry = %+v", owner)
	}
	var found bool
	for _, m := range members {
		found = found || m.Role == subagent.RoleRoot && m.Name == "root" && m.Service
	}
	if !found {
		t.Fatalf("members miss the internal root: %+v", members)
	}
	back, err := e.svc.ReturnSeat(e.ctx, rec.ID, "")
	if err != nil || back.Seat.External {
		t.Fatalf("ReturnSeat = %+v, %v", back, err)
	}
	if members, _ = e.svc.Members("alpha", false); members[0].Seat.External {
		t.Fatalf("seat after ReturnSeat = %+v", members[0].Seat)
	}
}
