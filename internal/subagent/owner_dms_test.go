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

func TestQuietMergeStewardReachesRootNextTurnOnce(t *testing.T) {
	testQuietServiceNextTurn(t, "", "merge-decision", "[MERGE STEWARD]")
}

func testQuietServiceNextTurn(t *testing.T, sender, kind, heading string) {
	t.Helper()
	e := newTestEnv(t)
	fixture := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateMeta(fixture.ID, func(m *Meta) { m.Role, m.Name, m.Depth = RoleRoot, RoleRoot, 0 }); err != nil {
		t.Fatal(err)
	}
	root, err := e.m.Store.Info(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sender == "" {
		sender = root.Room + ":merge-steward"
	}
	text := "Goals journal j0:\n- @lead · on_track · Tests pass. (g1)"
	if kind == "merge-decision" {
		text = "Return #17; the queue stays paused until the failed test is fixed."
	}
	entry, err := e.m.RoomStore().Post(root.RoomID, room.PostInput{From: sender, Kind: kind, To: []string{root.Room}, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	entries := []room.Message{entry}
	if kind == "merge-decision" {
		for _, quietKind := range []string{"notice", "pr"} {
			msg, err := e.m.RoomStore().Post(root.RoomID, room.PostInput{From: sender, Kind: quietKind, To: []string{root.Room}, Text: "Quiet " + quietKind + " context"})
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, msg)
		}
	}
	for _, msg := range entries {
		if msg.Wake || msg.NeedsRoot || room.WakesRoot(msg, root.Room) {
			t.Fatalf("quiet context acquired wake intent: %+v", msg)
		}
	}
	if got := forSession(e.m.RoomStore(), root.Meta, entries); len(got) != 0 {
		t.Fatalf("quiet context entered the live root gate: %+v", got)
	}
	end, err := e.m.RoomStore().FileSize(root.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if meta, _ := e.m.Store.ReadMeta(root.ID); meta.Turns != 1 {
		t.Fatal("a quiet journal entry woke root")
	}
	if _, err := e.send(root.ID, "RESPOND: next", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(root.ID, 2)
	if got := fmt.Sprint(turn.Result); strings.Count(got, heading) != 1 || !strings.Contains(got, text) || !strings.Contains(got, entry.ID) {
		t.Fatalf("journal block missing:\n%s", got)
	}
	for _, msg := range entries {
		if got := fmt.Sprint(turn.Result); !strings.Contains(got, msg.ID) || !strings.Contains(got, msg.Text) {
			t.Fatalf("quiet context missing from the real next turn: %s", got)
		}
	}
	// The note cursor moved past the entry, so no later turn lists it again.
	if meta, _ := e.m.Store.ReadMeta(root.ID); meta.OwnerDMCursor == nil || *meta.OwnerDMCursor < end {
		t.Fatalf("note cursor %v did not pass the entry at %d", meta.OwnerDMCursor, end)
	}
}

func TestQuietRootDigestIsBoundedAndExcludesActionablePosts(t *testing.T) {
	var posts []room.Message
	for i := range 35 {
		posts = append(posts, room.Message{ID: fmt.Sprintf("quiet-%02d", i), From: "work:author", Kind: "message", To: []string{"work"}, Text: strings.Repeat("界", 300)})
	}
	posts = append(posts, room.Message{Wake: true, ID: "action", From: "work:author", Kind: "message", To: []string{"work"}, NeedsRoot: true, Text: "Decide now."}, room.Message{Wake: true, ID: "human", From: "work", Human: true, Kind: "message", To: []string{"work"}, Text: "Owner request."}, room.Message{Wake: true, ID: "own", From: "work:root", Kind: "message", To: []string{"work"}, Text: "Root reply."})
	digest := quietRootPosts("work", posts)
	if !strings.Contains(digest, "5 older quiet posts omitted") || strings.Count(digest, "- work:author:") != 30 || strings.Contains(digest, "quiet-04") || !strings.Contains(digest, "quiet-34") || strings.Contains(digest, "Decide now.") || strings.Contains(digest, "Owner request.") || strings.Contains(digest, "Root reply.") || strings.Contains(digest, strings.Repeat("界", 241)) {
		t.Fatalf("incorrect quiet digest: %s", digest)
	}
}

// Every quiet kind addressed to root reaches the internal root: issues and
// completions land in the quiet digest, while merge decisions keep their own section. A marked action request stays turn input, not context.
func TestQuietSectionsCoverEveryRootAddressedKind(t *testing.T) {
	posts := []room.Message{
		{ID: "merge", From: "work:merge-steward", Kind: "merge-decision", To: []string{"work"}, Text: "Return #17"},
		{ID: "issue", From: "work:author", Kind: "issue", To: []string{"work"}, Text: "Repair the test"},
		{ID: "done", From: "work:author", Kind: "completed", OwnerReport: true, To: []string{"work"}, Text: "Report shipped"},
		{ID: "action", Wake: true, From: "work:author", Kind: "message", To: []string{"work"}, NeedsRoot: true, Text: "Decide now."},
	}
	digest := quietRootPosts("work", posts)
	for _, id := range []string{"issue", "done"} {
		if !strings.Contains(digest, id) {
			t.Errorf("quiet digest misses %s:\n%s", id, digest)
		}
	}
	for _, id := range []string{"merge", "action"} {
		if strings.Contains(digest, id) {
			t.Errorf("quiet digest lists %s, which has another home:\n%s", id, digest)
		}
	}
	notes := strings.Join(quietServiceNotes("work", posts), "\n")
	for _, id := range []string{"merge"} {
		if !strings.Contains(notes, id) {
			t.Errorf("service notes miss %s:\n%s", id, notes)
		}
	}
}

func TestPassiveRootInputHasOneHome(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: "ses_root", Name: room.RootAgent, Role: RoleRoot, Room: rec.Name, RoomID: rec.ID}
	note := room.Message{ID: "passive", From: "dyna", Kind: "instructions", To: []string{"work:root"}, Text: "Updated rules"}
	posts := []room.Message{note}
	if input := forSession(rooms, meta, posts); len(input) != 1 || input[0].ID != note.ID {
		t.Fatalf("passive engine note must remain turn input: %+v", input)
	}
	if digest := quietRootPosts(meta.Room, posts); digest != "" {
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
	messages := []room.Message{
		{ID: "quiet-owner", Wake: true, From: "work:author", Kind: "message", To: []string{"work"}},
		{ID: "quiet-direct", Wake: true, From: "work:author", Kind: "message", To: []string{"work:root"}},
		{ID: "action", Wake: true, From: "work:author", Kind: "message", To: []string{"work:root"}, NeedsRoot: true},
		{ID: "notice", Kind: "notice", Wake: true, From: "work:author", To: []string{"work"}, NeedsRoot: true},
		{ID: "human", Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work"}},
		{ID: "failure", Wake: true, From: "work:author", Kind: "failed", To: []string{"work"}, OwnerReport: true},
	}
	got := forSession(rooms, meta, messages)
	if len(got) != 4 || got[0].ID != "action" || got[1].ID != "notice" || got[2].ID != "human" || got[3].ID != "failure" {
		t.Fatalf("root gate input: %+v", got)
	}
}
