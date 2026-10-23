package subagent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
)

// The delivery rows replaced the session_queues blob, so a test that puts a
// session in a claim state a long run would reach uses these helpers instead of
// writing the blob.

// claimsOf reads the claim state one session has in its room, as the readers
// see it.
func claimsOf(t *testing.T, e *testEnv, id string) deliveryClaims {
	t.Helper()
	claims, err := sessionClaims(e.m.rooms(), metaOf(t, e, id))
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

// metaOf reads one session's meta.
func metaOf(t *testing.T, e *testEnv, id string) Meta {
	t.Helper()
	meta, err := e.m.Store.ReadMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	return meta
}

// claimPostsFor claims postIDs for turn, as turn allocation and an input claim
// do.
func claimPostsFor(t *testing.T, e *testEnv, id string, turn int, postIDs ...string) {
	t.Helper()
	if _, err := e.m.Store.claimPosts(context.Background(), e.m.rooms(), metaOf(t, e, id), postIDs, turn); err != nil {
		t.Fatal(err)
	}
}

// cancelPostFor cancels one queued delivery of the session.
func cancelPostFor(t *testing.T, e *testEnv, id, postID string) bool {
	t.Helper()
	cancelled, err := e.m.Store.cancelPost(context.Background(), e.m.rooms(), metaOf(t, e, id), postID)
	if err != nil {
		t.Fatal(err)
	}
	return cancelled
}

// dismissPostFor dismisses one pending delivery of the session.
func dismissPostFor(t *testing.T, e *testEnv, id, postID string) bool {
	t.Helper()
	dismissed, err := e.m.Store.dismissPost(context.Background(), e.m.rooms(), metaOf(t, e, id), postID)
	if err != nil {
		t.Fatal(err)
	}
	return dismissed
}

// insertDeliveryRow adds a delivery row directly, for a state no post can
// reach today, such as a claim on a post the room log no longer holds.
func insertDeliveryRow(t *testing.T, e *testEnv, id, postID string, state room.DeliveryState, turn int) {
	t.Helper()
	meta := metaOf(t, e, id)
	if err := e.m.rooms().DeliveryWrite(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `INSERT OR REPLACE INTO post_deliveries
(room_id, post_id, session_id, mode, category, state, claimed_turn, updated_at)
VALUES(?, ?, ?, 'wake', '', ?, ?, ?)`,
			meta.RoomID, postID, meta.ID, string(state), turn, time.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// touchDelivery writes one delivery row of session id, which moves the members
// revision the way a claim does.
func touchDelivery(t *testing.T, s *Store, id string) {	t.Helper()
	dataDir := filepath.Dir(s.root())
	if err := room.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var at int64
	if err := db.QueryRow(`INSERT INTO post_deliveries(room_id, post_id, session_id, mode, category, state, claimed_turn, updated_at)
VALUES('room', 'msg_a', ?, 'wake', '', 'claimed', 1,
  (SELECT COALESCE(MAX(updated_at), 0) + 1 FROM post_deliveries))
RETURNING updated_at`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
}

// A post a finished turn received is never claimed again. State alone cannot
// tell: the one-time post index import marks every retained row delivered
// (conversation PR 4b part 1), so the row cannot say whether its recipient read
// it. The turn receipts are the guard, and a later turn orders its input by
// them: drainRoomMessages skips a post a turn already delivered, and Wake asks
// deliveredPostTurn before it starts a turn.
func TestDeliveredPostIsNotClaimedByTheNextTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	e.wait(info.ID, 1)
	before, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A quiet post waits for the next turn instead of waking one, so the test
	// decides which turn reads it.
	post, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: info.Room, To: []string{info.Address()}, Text: "received once"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: second", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, second := e.wait(info.ID, 2)
	if !second.DeliveredPost(post.ID) {
		t.Fatalf("turn 2 steering = %#v", second.Steering)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, post.ID); row.ClaimedTurn != 2 || row.State != room.DeliveryDelivered {
		t.Fatalf("row = %+v; want the turn that received it, 2, delivered", row)
	}
	// A runner that died before committing its cursor leaves turn 3 reading the
	// same posts again. It must not claim the one turn 2 received.
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.RoomCursor = before.RoomCursor }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: third", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, third := e.wait(info.ID, 3)
	if third.DeliveredPost(post.ID) || strings.Contains(third.Prompt, "received once") {
		t.Fatalf("turn 3 received the post again: %#v", third.Steering)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, post.ID); row.ClaimedTurn != 2 {
		t.Fatalf("turn 3 claimed a delivered post: row = %+v, want claim turn 2", row)
	}
}
