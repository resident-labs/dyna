package subagent

import (
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// An owner DM sent while a root turn runs is skipped by that turn's live gate,
// which still moves the room cursor past it. The root's next turn with posts
// lists it exactly once, and the DM itself never starts a turn.
func TestOwnerDMDuringActiveRootTurnReachesNextTurnOnce(t *testing.T) {
	e := newTestEnv(t)
	root, _ := e.start("SLEEP:0\nRESPOND: ready", func(o *StartOptions) { o.Role, o.Name = RoleRoot, RoleRoot })
	e.wait(root.ID, 1)
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	active, err := e.m.beginTurn(root.ID, "RESPOND: active", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	e.m.Spawner = runner
	meta, err := e.m.Store.ReadMeta(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate := e.m.Store.newTurnGate(e.m.RoomStore(), meta, active.N, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	post := func(to, text string) room.Message {
		msg, err := e.m.RoomStore().Post(root.RoomID, room.PostInput{From: root.Room, Human: true, To: []string{to}, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	dm := post(root.Room+":voice", "Yes, delete the old voice files.")
	steer := post(root.Address(), "RESPOND: steer")
	got, _, err := gate.Read(0, false)
	if err != nil || len(got) != 1 || got[0].ID != steer.ID {
		t.Fatalf("live gate read = %+v, %v; want only the steer", got, err)
	}
	// The turn ends: its runner saves the gate's cursor, past the DM.
	if _, err := e.m.Store.UpdateMeta(root.ID, func(m *Meta) { m.RoomCursor = gate.cursor }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(root.ID, active.N, func(t *Turn) { t.Status = TurnOK }); err != nil {
		t.Fatal(err)
	}
	held.lock.Close()
	next := active.N + 1
	if _, err := e.send(root.ID, "SLEEP:0\nRESPOND: next", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(root.ID, next)
	if n := strings.Count(fmt.Sprint(turn.Result), dm.ID); n != 1 {
		t.Fatalf("turn %d lists the DM %d times:\n%s", next, n, turn.Result)
	}
	if _, err := e.send(root.ID, "SLEEP:0\nRESPOND: later", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	info, later := e.wait(root.ID, next+1)
	if strings.Contains(fmt.Sprint(later.Result), dm.ID) {
		t.Fatalf("turn %d lists the DM again:\n%s", next+1, later.Result)
	}
	if info.Turns != next+1 {
		t.Fatalf("turns = %d, want %d: the DM started a turn", info.Turns, next+1)
	}
}

// seatRowsFor builds the seat delivery rows of msgs as the writer stores them,
// so these rendering tests read the same rows the runner reads.
func seatRowsFor(roomName string, msgs []room.Message) map[string]room.Delivery {
	out := map[string]room.Delivery{}
	for _, msg := range msgs {
		for _, d := range room.DeliveriesFor(msg, roomName) {
			if d.SessionID == room.SeatSessionID(msg.RoomID) {
				out[msg.ID] = d
			}
		}
	}
	return out
}

func TestQuietRootDigestIsBoundedAndExcludesActionablePosts(t *testing.T) {
	var posts []room.Message
	for i := range 35 {
		posts = append(posts, room.Message{ID: fmt.Sprintf("quiet-%02d", i), From: "work:author", Kind: "message", To: []string{"work"}, Text: strings.Repeat("界", 300)})
	}
	posts = append(posts, room.Message{Wake: true, ID: "action", From: "work:author", Kind: "message", To: []string{"work"}, NeedsRoot: true, Text: "Decide now."}, room.Message{Wake: true, ID: "human", From: "work", Human: true, Kind: "message", To: []string{"work"}, Text: "Owner request."}, room.Message{Wake: true, ID: "own", From: "work:root", Kind: "message", To: []string{"work"}, Text: "Root reply."})
	digest := quietRootPosts("work", posts, seatRowsFor("work", posts))
	if !strings.Contains(digest, "5 older quiet posts omitted") || strings.Count(digest, "- work:author:") != 30 || strings.Contains(digest, "quiet-04") || !strings.Contains(digest, "quiet-34") || strings.Contains(digest, "Decide now.") || strings.Contains(digest, "Owner request.") || strings.Contains(digest, "Root reply.") || strings.Contains(digest, strings.Repeat("界", 241)) {
		t.Fatalf("incorrect quiet digest: %s", digest)
	}
}

// Every quiet kind addressed to root reaches the internal root: issues and
// completions land in the quiet digest. A marked action request stays turn
// input, not context.
func TestQuietSectionsCoverEveryRootAddressedKind(t *testing.T) {
	posts := []room.Message{
		{ID: "issue", From: "work:author", Kind: "issue", To: []string{"work"}, Text: "Repair the test"},
		{ID: "done", From: "work:author", Kind: "completed", OwnerReport: true, To: []string{"work"}, Text: "Report shipped"},
		{ID: "action", Wake: true, From: "work:author", Kind: "message", To: []string{"work"}, NeedsRoot: true, Text: "Decide now."},
	}
	digest := quietRootPosts("work", posts, seatRowsFor("work", posts))
	for _, id := range []string{"issue", "done"} {
		if !strings.Contains(digest, id) {
			t.Errorf("quiet digest misses %s:\n%s", id, digest)
		}
	}
	if strings.Contains(digest, "action") {
		t.Errorf("quiet digest lists action, which is turn input:\n%s", digest)
	}
}

func TestPassiveRootInputHasOneHome(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: "ses_root", Name: room.RootAgent, Role: RoleRoot, Room: rec.Name, RoomID: rec.ID}
	note, err := rooms.PostEvent(rec.ID, room.SessionEvent{Type: room.SessionInstructions, Session: "work:lead", Detail: "Updated rules"}, "work:root")
	if err != nil {
		t.Fatal(err)
	}
	posts := []room.Message{note}
	if input := forSession(rooms, meta, posts); len(input) != 1 || input[0].ID != note.ID {
		t.Fatalf("passive engine note must remain turn input: %+v", input)
	}
	if digest := quietRootPosts(meta.Room, posts, seatRowsFor(meta.Room, posts)); digest != "" {
		t.Fatalf("passive input repeated in the quiet digest: %s", digest)
	}
}

func TestRootGateRequiresActionIntentForDirectWorkerCopies(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: "ses_root", Name: room.RootAgent, Role: RoleRoot, Room: rec.Name, RoomID: rec.ID}
	var messages []room.Message
	for _, in := range []room.PostInput{
		{Wake: true, From: "work:author", Kind: "message", To: []string{"work"}, Text: "quiet owner"},
		{Wake: true, From: "work:author", Kind: "message", To: []string{"work:root"}, Text: "quiet direct"},
		{Wake: true, From: "work:author", Kind: "message", To: []string{"work:root"}, NeedsRoot: true, Text: "action"},
		{Wake: true, From: "work:author", Kind: "message", To: []string{"work"}, NeedsRoot: true, Text: "notice"},
		{Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work"}, Text: "human"},
		{Wake: true, From: "work:author", Kind: "failed", OwnerReport: true, To: []string{"work"}, Text: "failure"},
	} {
		msg, err := rooms.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	got := forSession(rooms, meta, messages)
	if len(got) != 4 || got[0].Text != "action" || got[1].Text != "notice" || got[2].Text != "human" || got[3].Text != "failure" {
		t.Fatalf("root gate input: %+v", got)
	}
}
