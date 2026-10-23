package subagent

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
)

// A session reads its input and its wakes from its stored delivery rows. This
// table pins the decision every kind of post got from the address strings
// before, so a reader that stops agreeing with the writer shows up here.
//
// delivered is whether the post reached this session at all, which the row
// says; input is whether the session reads it as turn input; and wakes is
// whether it asks the session for a turn.
func TestSessionInputFromDeliveryRows(t *testing.T) {
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	t.Cleanup(func() { database.Close(filepath.Dir(rooms.Root)) })
	rec, err := rooms.Create(room.CreateInput{Name: "dialogue"})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: "ses_20261023-120000-11111111", Name: "worker", Room: rec.Name, RoomID: rec.ID}
	parent := Meta{ID: "ses_20261023-120000-22222222", Name: "parent", Room: rec.Name, RoomID: rec.ID}
	addr, parentAddr, oldAddr := meta.Address(), parent.Address(), rec.Name+":old-name"
	post := func(in room.PostInput) room.Message {
		t.Helper()
		msg, err := rooms.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	event := func(ev room.Event, to string) room.Message {
		t.Helper()
		msg, err := rooms.PostEvent(rec.ID, ev, to)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	to := func(address, session string) room.PostInput {
		in := room.PostInput{Wake: true, From: rec.Name, To: []string{address}, Text: "for " + address}
		if session != "" {
			in.SessionIDs = map[string]string{address: session}
		}
		return in
	}
	closed := post(to(addr, meta.ID))
	meta.ClosedPosts = map[string]bool{closed.ID: true}

	for _, tc := range []struct {
		name      string
		msg       room.Message
		delivered bool
		input     bool
		wakes     bool
	}{
		{"bound recipient", post(to(addr, meta.ID)), true, true, true},
		{"renamed session", post(to(oldAddr, meta.ID)), true, true, true},
		{"unbound address", post(to(addr, "")), true, true, true},
		{"address held by another session", post(to(addr, parent.ID)), false, false, false},
		{"owner post to the room", post(room.PostInput{Wake: true, From: rec.Name, To: []string{rec.Name}, Text: "to the room"}), false, false, false},
		{"own post", post(room.PostInput{Wake: true, From: addr, To: []string{addr}, SessionIDs: map[string]string{addr: meta.ID}, Text: "my own words"}), true, false, true},
		{"consolidation inbox", post(to(parentAddr, parent.ID)), false, false, false},
		{"quiet post", post(room.PostInput{From: rec.Name, To: []string{addr}, SessionIDs: map[string]string{addr: meta.ID}, Text: "for the next turn"}), true, true, false},
		{"quiet notice", event(room.SessionEvent{Type: room.SessionRenamed, Session: addr, Name: "worker", OldName: "old-name"}, addr), true, false, false},
		{"instruction note", event(room.SessionEvent{Type: room.SessionInstructions, Session: addr, Detail: "use short sentences"}, addr), true, true, false},
		{"reopened session", closed, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := inputRows(rooms, meta.RoomID, meta.ID, addr, []string{tc.msg.ID})
			_, delivered := rows[tc.msg.ID]
			if delivered != tc.delivered {
				t.Fatalf("delivered = %v, want %v", delivered, tc.delivered)
			}
			input := len(forSession(rooms, meta, []room.Message{tc.msg})) == 1
			if input != tc.input {
				t.Fatalf("input = %v, want %v", input, tc.input)
			}
			if wakes := delivered && wakes(rows, tc.msg); wakes != tc.wakes {
				t.Fatalf("wakes = %v, want %v", wakes, tc.wakes)
			}
		})
	}
}
