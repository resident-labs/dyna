package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
)

// sessionFixture creates a session record and its turn folder.
func sessionFixture(t *testing.T, e *testEnv) (string, string) {
	t.Helper()
	id := NewID()
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	return id, dir
}

func writeSession(t *testing.T, e *testEnv, id string, cursor int64, turns int) Meta {
	t.Helper()
	now := time.Now()
	meta := Meta{ID: id, Name: "fixture-" + id, RoomID: e.room.ID, Room: e.room.Name,
		RoomCursor: cursor, Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now,
		LastActivity: now, Turns: turns, Resumable: true, HarnessSession: "mock-" + id, Status: StatusIdle}
	if err := e.m.Store.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

// carriedTurnFixture prepares an accepted turn that carried a post through the
// same writers the runner uses: startTurn records the carried input, noteTurn
// records the harness acceptance, and endTurn records the terminal turn, which
// clears PendingInputIDs. The room cursor stays before the post, as after a
// crash that did not commit it. The unaccepted variant ends through the real
// repair writer instead.
func carriedTurnFixture(t *testing.T, e *testEnv, accepted bool) (Info, room.Message) {
	t.Helper()
	id, dir := sessionFixture(t, e)
	beforePost, err := e.m.rooms().FileSize(e.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := writeSession(t, e, id, beforePost, 2)
	now := time.Now()
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnQueued, Prompt: "RESPOND: first"}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.endTurn(e.ctx, id, TurnEnd{Turn: 1, Status: TurnOK, Ended: now,
		HarnessSession: meta.HarnessSession, InputCutoff: &beforePost}); err != nil {
		t.Fatal(err)
	}
	msg := e.postTo(meta)
	afterPost, err := e.m.rooms().FileSize(e.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(id, Turn{N: 2, Status: TurnQueued}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.startTurn(e.ctx, id, TurnStart{Turn: 2, Running: true,
		Drained: []DrainedPost{{Steer: Steer{ID: msg.ID, Message: msg.Text}}}}); err != nil {
		t.Fatal(err)
	}
	if accepted {
		if err := e.m.noteTurn(e.ctx, id, TurnNote{Turn: 2, Accepted: true}); err != nil {
			t.Fatal(err)
		}
		if err := e.m.endTurn(e.ctx, id, TurnEnd{Turn: 2, Status: TurnOK, Ended: now,
			InputCutoff: &afterPost}); err != nil {
			t.Fatal(err)
		}
	} else if _, err := e.m.Store.RepairTurn(id, 2, "runner exited without recording a result"); err != nil {
		t.Fatal(err)
	}
	claim(t, e, dir, msg.ID, 2)
	return e.info(id), msg
}

// postTo stores one waking post for meta's session.
func (e *testEnv) postTo(meta Meta) room.Message {
	e.t.Helper()
	msg, err := e.m.rooms().Post(e.room.ID, room.PostInput{Wake: true, From: e.room.Name,
		To: []string{meta.Address()}, Text: "SLEEP:0\nRESPOND: carried"})
	if err != nil {
		e.t.Fatal(err)
	}
	return msg
}

func claim(t *testing.T, e *testEnv, dir, messageID string, turn int) {
	t.Helper()
	if err := writeQueue(dir, queueClaims{Claimed: map[string]int{messageID: turn},
		Cancelled: map[string]bool{}, Dismissed: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) info(id string) Info {
	e.t.Helper()
	info, err := e.m.Store.Info(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return info
}

// A completed turn that carried a post proves delivery through its carried IDs
// after completion cleared PendingInputIDs. Excluding the steering proof shows
// the carried record alone carries the proof, and the control fails without it.
func TestCarriedIDsProveDeliveryAfterCompletion(t *testing.T) {
	e := newTestEnv(t)
	info, msg := carriedTurnFixture(t, e, true)
	turn, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.PendingInputIDs) != 0 {
		t.Fatalf("completion left pending input: %v", turn.PendingInputIDs)
	}
	turn.Steering = nil // the steering record is not the proof under test
	if !turn.DeliveredPost(msg.ID) {
		t.Fatalf("the carried record alone does not prove delivery: %+v", turn)
	}
	without := turn
	without.CarriedIDs = nil
	if without.DeliveredPost(msg.ID) {
		t.Fatal("delivery is proven without a carried record")
	}
	for i := 1; i <= 2; i++ {
		res, err := e.m.Wake(e.ctx, info.ID, WakeOptions{MessageID: msg.ID})
		if err != nil {
			t.Fatalf("wake %d: %v", i, err)
		}
		if !res.Steered || res.Turn != 2 {
			t.Fatalf("wake %d = %+v, want the carried turn 2", i, res)
		}
	}
	if got, err := e.m.Store.ReadMeta(info.ID); err != nil || got.Turns != 2 {
		t.Fatalf("turns = %d, %v; want 2", got.Turns, err)
	}
}

// An accepted turn that an orphan repair ends still counts as delivered: the
// harness read the prompt, so its carried post must not wake a second turn.
func TestAcceptedRepairedTurnStillDelivers(t *testing.T) {
	e := newTestEnv(t)
	id, dir := sessionFixture(t, e)
	cursor, err := e.m.rooms().FileSize(e.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := writeSession(t, e, id, cursor, 1)
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnQueued}); err != nil {
		t.Fatal(err)
	}
	msg := e.postTo(meta)
	if err := e.m.startTurn(e.ctx, id, TurnStart{Turn: 1, Running: true,
		Drained: []DrainedPost{{Steer: Steer{ID: msg.ID, Message: msg.Text}}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.noteTurn(e.ctx, id, TurnNote{Turn: 1, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	claim(t, e, dir, msg.ID, 1)
	if _, err := e.m.Store.RepairTurn(id, 1, "runner exited without recording a result"); err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(id, 1)
	if err != nil || turn.Status != TurnError || !turn.Accepted {
		t.Fatalf("repaired turn = %+v, %v", turn, err)
	}
	if len(turn.PendingInputIDs) != 0 {
		t.Fatalf("repair left pending input: %v", turn.PendingInputIDs)
	}
	turn.Steering = nil
	if !turn.DeliveredPost(msg.ID) {
		t.Fatalf("an accepted repaired turn lost its carried proof: %+v", turn)
	}
	res, err := e.m.Wake(e.ctx, id, WakeOptions{MessageID: msg.ID})
	if err != nil || !res.Steered || res.Turn != 1 {
		t.Fatalf("wake = %+v, %v; want the repaired turn 1", res, err)
	}
	if got, err := e.m.Store.ReadMeta(id); err != nil || got.Turns != 1 {
		t.Fatalf("turns = %d, %v; want 1", got.Turns, err)
	}
}

// A turn a repair ended before any harness prompt never accepted, so its
// claimed post must stay retryable even though the turn claimed and carried it.
func TestRepairedTurnKeepsItsClaimedPostRetryable(t *testing.T) {
	e := newTestEnv(t)
	info, msg := carriedTurnFixture(t, e, false)
	turn, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil || turn.Accepted || turn.DeliveredPost(msg.ID) {
		t.Fatalf("repaired turn = %+v, %v; want no delivery proof", turn, err)
	}
	res, err := e.m.Wake(e.ctx, info.ID, WakeOptions{MessageID: msg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steered || res.Turn != 3 {
		t.Fatalf("wake = %+v, want a new turn for the retried post", res)
	}
	if got, err := e.m.Store.ReadMeta(info.ID); err != nil || got.Turns != 3 {
		t.Fatalf("turns = %d, %v; want 3", got.Turns, err)
	}
}

// A post an accepted turn carried must not enter a later turn's prompt again,
// even when the room cursor still points before it. The room-log read returns
// it, so the selection must skip a proven claim there too.
func TestCarriedPostIsNotDrainedAgain(t *testing.T) {
	e := newTestEnv(t)
	info, msg := carriedTurnFixture(t, e, true)
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.m.pendingWakePosts(meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range posts {
		if post.ID == msg.ID {
			t.Fatalf("carried post %s entered another turn", post.ID)
		}
	}
}

// A claimed post whose turn cannot be read must not be delivered again. The
// steering copy covers the earlier turns, so only the drain's own read of the
// claimed turn fails, and the drain must fail closed.
func TestUnreadableClaimedTurnFailsClosed(t *testing.T) {
	e := newTestEnv(t)
	info, msg := carriedTurnFixture(t, e, false)
	// A third turn lets the steering table copy turns 1 and 2, so the drain
	// stops reading their rows itself.
	now := time.Now()
	if err := e.m.Store.writeTurn(info.ID, Turn{N: 3, Status: TurnOK, Ended: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Turns = 3 }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.steering(info.ID, 3); err != nil {
		t.Fatalf("prepare the steering copy: %v", err)
	}
	// The claimed turn has no canonical row.
	db, err := database.Open(filepath.Dir(e.m.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_turns WHERE session = ? AND n = ?", info.ID, 2); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.m.pendingWakePosts(meta)
	if err == nil {
		t.Fatalf("pendingWakePosts returned %d posts and no error; want the unreadable turn error", len(posts))
	}
	for _, post := range posts {
		if post.ID == msg.ID {
			t.Fatalf("claimed post %s was redelivered after the read failed", post.ID)
		}
	}
}
