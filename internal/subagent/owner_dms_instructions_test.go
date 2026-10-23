package subagent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// instructionRoom stores instruction notes in a room named after the engine, so
// their sender carries the same address as the room, as the real burst did. The
// clock moves only when a test says so, so a fold never depends on the wall.
type instructionRoom struct {
	store *room.Store
	rec   room.Record
	msgs  []room.Message
	now   time.Time
	// changes counts the changes stored, so two changes of one section name
	// their sessions apart.
	changes int
}

func newInstructionRoom(t *testing.T) *instructionRoom {
	t.Helper()
	store := &room.Store{Root: t.TempDir()}
	r := &instructionRoom{store: store, now: time.Date(2026, 10, 23, 21, 0, 0, 0, time.UTC)}
	store.Now = func() time.Time { return r.now }
	rec, err := store.Create(room.CreateInput{Name: room.EngineSender})
	if err != nil {
		t.Fatal(err)
	}
	r.rec = rec
	return r
}

// change stores one note per session, all of one change of one section. The
// occurrence rises with every change, as the real producer's does, so two edits
// of one section are two notes for the same session.
func (r *instructionRoom) change(t *testing.T, section string, sessions int) {
	t.Helper()
	for i := range sessions {
		ev := room.SessionEvent{
			Type:       room.SessionInstructions,
			Session:    fmt.Sprintf("%s:w%02d", r.rec.Name, i),
			Sections:   []room.InstructionChange{{Name: section, Action: room.InstructionItems, Added: []string{"changed"}}},
			Occurrence: r.changes + 1,
		}
		msg, err := r.store.PostEvent(r.rec.ID, ev, ev.Session)
		if err != nil {
			t.Fatal(err)
		}
		r.msgs = append(r.msgs, msg)
	}
	r.changes++
}

// One instruction edit reaches every session at once. The root seat reads one
// folded line about it, and no note looks like an owner DM: the owner wrote
// none of them.
func TestInstructionBurstIsOneLineAndNoOwnerDM(t *testing.T) {
	r := newInstructionRoom(t)
	r.change(t, "Subagent profiles", 35)
	seats := seatRowsFor(r.rec.Name, r.msgs)
	if note := ownerDMNote(r.rec.Name, r.msgs, seats); note != "" {
		t.Fatalf("the instruction burst reached the owner DM block:\n%s", note)
	}
	if digest := quietRootPosts(r.rec.Name, r.msgs, seats); digest != "" {
		t.Fatalf("the instruction burst reached the quiet digest:\n%s", digest)
	}
	if lines := instructionLines(r.msgs); len(lines) != 1 || lines[0] != "- Instructions changed for 35 sessions: Subagent profiles." {
		t.Fatalf("folded lines = %q", lines)
	}
	// The root seat's own context carries the one line and nothing per session.
	notes := strings.Join(seatNotes(r.store, Meta{RoomID: r.rec.ID, Room: r.rec.Name}, r.msgs), "\n")
	if !strings.Contains(notes, "Instructions changed for 35 sessions: Subagent profiles.") || strings.Count(notes, "Instructions changed") != 1 {
		t.Fatalf("seat notes = %q", notes)
	}
}

// A note stored before this fix still has an owner-DM row in the seat's stored
// deliveries. The digest leaves it out and folds it with the others, so the
// owner's own words stay the only owner DMs.
func TestOwnerDMBlockLeavesOutStoredInstructionRows(t *testing.T) {
	r := newInstructionRoom(t)
	r.change(t, "Subagent profiles", 3)
	stored := map[string]room.Delivery{}
	for _, msg := range r.msgs {
		stored[msg.ID] = room.Delivery{SessionID: room.SeatSessionID(r.rec.ID), Mode: room.DeliveryQuiet, Category: room.RootOwnerDM}
	}
	if note := ownerDMNote(r.rec.Name, r.msgs, stored); note != "" {
		t.Fatalf("stored instruction rows read as owner DMs:\n%s", note)
	}
	if lines := instructionLines(r.msgs); len(lines) != 1 {
		t.Fatalf("folded lines = %q", lines)
	}
}

// Two edits are two changes. A second edit of one section ten seconds later
// reaches the same sessions again, so it is its own line and never counts those
// sessions twice; another section is its own line too.
func TestInstructionFoldingKeepsASecondChangeApart(t *testing.T) {
	r := newInstructionRoom(t)
	r.change(t, "Subagent profiles", 35)
	r.now = r.now.Add(10 * time.Second)
	r.change(t, "Subagent profiles", 35)
	r.now = r.now.Add(3 * time.Minute)
	r.change(t, "Session rules", 1)
	want := []string{
		"- Instructions changed for 35 sessions: Subagent profiles.",
		"- Instructions changed for 35 sessions: Subagent profiles.",
		"- Instructions changed for 1 session: Session rules.",
	}
	if lines := instructionLines(r.msgs); fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("folded lines = %q, want %q", lines, want)
	}
}
