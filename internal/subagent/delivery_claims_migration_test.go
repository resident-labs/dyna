package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
)

// writeQueueBlob stores a retired session_queues row, as a store that has not
// run this release's import still holds.
func writeQueueBlob(t *testing.T, s *Store, id string, queue legacyQueueClaims) {
	t.Helper()
	db, err := queuesDB(s.root())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := saveQueueRow(context.Background(), conn, id, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// deliveryRowOf reads one session's row for one post.
func deliveryRowOf(t *testing.T, e *testEnv, id, postID string) (room.Delivery, bool) {
	t.Helper()
	meta := metaOf(t, e, id)
	rows, err := e.m.rooms().SessionDeliveries(meta.RoomID, room.DeliveryKeys(meta.ID, meta.Address()), []string{postID})
	if err != nil {
		t.Fatal(err)
	}
	row, ok := rows[postID]
	return row, ok
}

// The one-time import moves a session's claims, cancellations and dismissals
// from the retired queue blob into its delivery rows, and never moves a row
// backwards.
func TestDeliveryClaimsImportMovesQueueStateIntoRows(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	claimed := postClaimInput(t, e, info, "claimed before the import")
	cancelled := postClaimInput(t, e, info, "cancelled before the import")
	dismissed := postClaimInput(t, e, info, "dismissed before the import")
	delivered := postClaimInput(t, e, info, "delivered before the import")
	untouched := postClaimInput(t, e, info, "no claim before the import")
	claimPostsFor(t, e, info.ID, 1, delivered.ID)
	if _, err := e.m.rooms().MarkDelivered(context.Background(), room.DeliverySet{
		RoomID: info.RoomID, Keys: room.DeliveryKeys(info.ID, info.Address()),
	}, []string{delivered.ID}); err != nil {
		t.Fatal(err)
	}
	writeQueueBlob(t, e.m.Store, info.ID, legacyQueueClaims{
		Claimed:   map[string]int{claimed.ID: 4},
		Cancelled: map[string]bool{cancelled.ID: true},
		Dismissed: map[string]bool{dismissed.ID: true},
	})
	if err := migrateQueues(filepath.Dir(e.m.Store.root())); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		state room.DeliveryState
		turn  int
	}{
		claimed.ID:   {room.DeliveryClaimed, 4},
		cancelled.ID: {room.DeliveryCancelled, 0},
		dismissed.ID: {room.DeliveryDismissed, 0},
		delivered.ID: {room.DeliveryDelivered, 1},
		untouched.ID: {room.DeliveryQueued, 0},
	}
	for postID, want := range want {
		row, ok := deliveryRowOf(t, e, info.ID, postID)
		if !ok {
			t.Fatalf("post %s has no delivery row", postID)
		}
		if row.State != want.state || row.ClaimedTurn != want.turn {
			t.Fatalf("post %s = state %s, turn %d; want %s, turn %d", postID, row.State, row.ClaimedTurn, want.state, want.turn)
		}
	}
	// A second start does not import again: a later claim keeps its state.
	claimPostsFor(t, e, info.ID, 5, cancelled.ID)
	if err := migrateQueues(filepath.Dir(e.m.Store.root())); err != nil {
		t.Fatal(err)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, cancelled.ID); row.State != room.DeliveryCancelled {
		t.Fatalf("a cancelled post was imported again as %+v", row)
	}
}

// A claim committed after the import moves the rows, keeps its exact receipt
// across a reopen, and cannot revive the retired blob.
func TestPostMigrationRunnerClaimKeepsRowsAndReceipt(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	stale := postClaimInput(t, e, info, "cancelled earlier claim")
	msg := postClaimInput(t, e, info, "claim from canonical state")
	writeQueueBlob(t, e.m.Store, info.ID, legacyQueueClaims{
		Claimed:   map[string]int{stale.ID: 1},
		Cancelled: map[string]bool{stale.ID: true},
	})
	data := filepath.Dir(e.m.Store.root())
	if err := migrateQueues(data); err != nil {
		t.Fatal(err)
	}
	if err := state.MigrateRequests(data); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	rid := info.ID + "/post-migration-claim"
	ctx := state.ForRequestCall(callspec.WithRequest(actor, rid), "runner/"+rid, "RunnerClaimInput", []byte("fixture-call"))
	reply, err := e.m.RunnerClaimInput(ctx, InputClaim{Turn: 1, Startup: true})
	if err != nil || len(reply.Posts) != 1 || reply.Posts[0].Steer.ID != msg.ID {
		t.Fatalf("post-migration claim used stale state: %+v, %v", reply, err)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, msg.ID); row.State != room.DeliveryClaimed || row.ClaimedTurn != 1 {
		t.Fatalf("claimed post = %+v", row)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, stale.ID); row.State != room.DeliveryCancelled || row.ClaimedTurn != 0 {
		t.Fatalf("cancelled post = %+v", row)
	}
	wantReply, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	saved, done, err := state.For(db).RequestDone(t.Context(), "runner/"+rid)
	if err != nil || !done || string(saved) != string(wantReply) {
		t.Fatalf("claim receipt did not survive reopen: done=%v, %v", done, err)
	}
}

// The retained queue file of an older release still reaches the delivery rows:
// the import chain reads the file into the retired table, then into the rows.
func TestRetainedQueueFileReachesDeliveryRows(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	msg := postClaimInput(t, e, info, "claimed by a retained file")
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(legacyQueueClaims{Claimed: map[string]int{msg.ID: 3}, Cancelled: map[string]bool{}, Dismissed: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, queueFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateQueues(filepath.Dir(e.m.Store.root())); err != nil {
		t.Fatal(err)
	}
	if row, _ := deliveryRowOf(t, e, info.ID, msg.ID); row.State != room.DeliveryClaimed || row.ClaimedTurn != 3 {
		t.Fatalf("retained claim = %+v", row)
	}
}
