package subagent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/authority"
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

// setDeliveryRow replaces a post's delivery rows with one row of the session's,
// for a state no post reaches today, such as a claim on a post the room log no
// longer holds or a row a previous release's index import wrote.
func setDeliveryRow(t *testing.T, e *testEnv, id, postID string, state room.DeliveryState, turn int) {
	t.Helper()
	meta := metaOf(t, e, id)
	if err := e.m.rooms().DeliveryWrite(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, "DELETE FROM post_deliveries WHERE room_id=? AND post_id=?", meta.RoomID, postID); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO post_deliveries
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
func touchDelivery(t *testing.T, s *Store, id string) {
	t.Helper()
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
// tell: the one-time post index import writes imported rows, and a released
// index import wrote delivered for every retained row without checking the
// recipient's receipts. The turn receipts are the guard, and a later turn
// orders its input by them: drainRoomMessages skips a post a turn already
// delivered, and Wake asks deliveredPostTurn before it starts a turn.
func TestDeliveredPostIsNotClaimedByTheNextTurn(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	post := postClaimInput(t, e, info, "received once")
	// Turn 1 took the post, its row says delivered, and its receipt says the
	// harness read it.
	claimPostsFor(t, e, info.ID, 1, post.ID)
	meta := metaOf(t, e, info.ID)
	if _, err := e.m.rooms().MarkDelivered(context.Background(), deliverySet(meta), []string{post.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.Steering = append(turn.Steering, Steer{ID: post.ID, Delivered: true})
	}); err != nil {
		t.Fatal(err)
	}
	// A runner that died before committing its cursor leaves turn 2 reading the
	// same post again.
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Turns, m.RoomCursor = 2, 0 }); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(info.ID, Turn{N: 2, Status: TurnRunning}); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	got, err := e.m.RunnerClaimInput(ctx, InputClaim{Turn: 2, Startup: true, Depth: info.Depth, ParentSession: info.ParentSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Posts) != 0 {
		t.Fatalf("turn 2 claimed a post turn 1 received: %+v", got.Posts)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, post.ID); row.ClaimedTurn != 1 || row.State != room.DeliveryDelivered {
		t.Fatalf("row = %+v; want the turn that received it, 1, delivered", row)
	}
}

// A session that inherits another inbox's wake takes it, and the row it adds
// for that claim keeps the post's wake decision: the claim is not the wake
// decision. Without the wake, an unacknowledged inherited wake would stop
// asking for a turn.
func TestInheritedWakeKeepsItsWakeWhenClaimed(t *testing.T) {
	e := newTestEnv(t)
	source := completedFixtureSession(t, e)
	target := completedFixtureSession(t, e)
	post, err := e.m.rooms().Post(source.RoomID, room.PostInput{Wake: true, From: source.Room, To: []string{source.Address()}, Text: "inherited wake"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(target.ID, func(m *Meta) {
		m.InheritedInboxes = []InheritedInbox{{Session: source.ID, Address: source.Address()}}
	}); err != nil {
		t.Fatal(err)
	}
	// The target's turn took it and ended without a receipt.
	claimPostsFor(t, e, target.ID, 1, post.ID)
	row, ok := deliveryRowOf(t, e, target.ID, post.ID)
	if !ok || row.State != room.DeliveryClaimed || row.ClaimedTurn != 1 {
		t.Fatalf("inherited claim row = %+v, %v; want one claimed row", row, ok)
	}
	if row.Mode != room.DeliveryWake {
		t.Fatalf("inherited claim row = %+v; want the post's wake mode", row)
	}
	posts, err := e.m.pendingWakePosts(metaOf(t, e, target.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range posts {
		if pending.ID == post.ID {
			return
		}
	}
	t.Fatalf("unacknowledged inherited wake lost: %+v", posts)
}

// An imported row is not proof of delivery, so an owner can still drop a post
// that only the index knows about.
func TestOwnerDropsPendingImportedDelivery(t *testing.T) {
	for _, owner := range []string{"cancel", "dismiss"} {
		t.Run(owner, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			post := postClaimInput(t, e, info, "imported and pending")
			setDeliveryRow(t, e, info.ID, post.ID, room.DeliveryImported, 0)
			dropped := false
			if owner == "cancel" {
				dropped = cancelPostFor(t, e, info.ID, post.ID)
			} else {
				dropped = dismissPostFor(t, e, info.ID, post.ID)
			}
			if !dropped {
				t.Fatalf("%s of an imported delivery found nothing to drop", owner)
			}
			row, _ := deliveryRowOf(t, e, info.ID, post.ID)
			want := room.DeliveryCancelled
			if owner == "dismiss" {
				want = room.DeliveryDismissed
			}
			if row.State != want {
				t.Fatalf("row = %+v, want %s", row, want)
			}
		})
	}
}

// A note the held prompt covered is cancelled, so a second startup from an old
// cursor cannot deliver it. An imported note is covered the same way: the index
// import wrote its row before any turn claimed it, so a filter that only takes
// queued or claimed rows would leave it pending.
func TestCoveredImportedNoteIsNotDeliveredByTheNextStartup(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	note, err := e.m.rooms().PostEvent(info.RoomID, room.SessionEvent{
		Type: room.SessionInstructions, Session: info.Address(), Detail: "covered imported note",
	}, info.Address())
	if err != nil {
		t.Fatal(err)
	}
	setDeliveryRow(t, e, info.ID, note.ID, room.DeliveryImported, 0)
	// The runner exits before it commits the cursor, so the next startup reads
	// the note from the same place.
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.RoomCursor = 0 }); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	first, err := e.m.RunnerClaimInput(ctx, InputClaim{Turn: 1, Startup: true, Depth: info.Depth, Covered: []string{note.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Posts) != 0 {
		t.Fatalf("a covered note was claimed: %+v", first.Posts)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, note.ID); row.State != room.DeliveryCancelled {
		t.Fatalf("covered row = %+v; want it cancelled", row)
	}
	second, err := e.m.RunnerClaimInput(ctx, InputClaim{Turn: 1, Startup: true, Depth: info.Depth})
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range second.Posts {
		if post.Steer.ID == note.ID {
			t.Fatalf("a second startup delivered the covered note again: %+v", second.Posts)
		}
	}
}
