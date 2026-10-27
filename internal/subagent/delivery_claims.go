package subagent

import (
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strconv"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// A session's claim state lives in its delivery rows (post_deliveries), not in
// a queue blob: claimed_turn names the turn that took a post, and state is
// queued, claimed, delivered, cancelled or dismissed. room's delivery-state
// API is the only code that writes it, and the readers below give the shape
// the queue blob gave: the posts a turn holds, and the posts the owner
// cancelled or dismissed.

// deliveryClaims is one session's claim state in one room, keyed by post ID.
type deliveryClaims struct {
	// claimedTurn names the turn that holds each claimed post.
	claimedTurn map[string]int
	cancelled   map[string]bool
	dismissed   map[string]bool
}

// claimsFromList builds the claim state from every delivery row of one
// session.
func claimsFromList(rows []room.Delivery) deliveryClaims {
	out := deliveryClaims{
		claimedTurn: make(map[string]int, len(rows)),
		cancelled:   map[string]bool{},
		dismissed:   map[string]bool{},
	}
	for _, row := range rows {
		out.add(row)
	}
	return out
}

// claimsFromRows builds the claim state from delivery rows keyed by post ID.
func claimsFromRows(rows map[string]room.Delivery) deliveryClaims {
	out := deliveryClaims{
		claimedTurn: make(map[string]int, len(rows)),
		cancelled:   map[string]bool{},
		dismissed:   map[string]bool{},
	}
	for _, row := range rows {
		out.add(row)
	}
	return out
}

// add records one row's claim state.
func (c deliveryClaims) add(row room.Delivery) {
	switch row.State {
	case room.DeliveryClaimed:
		if row.ClaimedTurn > 0 {
			c.claimedTurn[row.PostID] = row.ClaimedTurn
		}
	case room.DeliveryCancelled:
		c.cancelled[row.PostID] = true
	case room.DeliveryDismissed:
		c.dismissed[row.PostID] = true
	}
}

// deliverySet names one session's delivery rows in its room.
func deliverySet(meta Meta) room.DeliverySet {
	return room.DeliverySet{RoomID: meta.RoomID, Keys: room.DeliveryKeys(meta.ID, meta.Address())}
}

// deliveryRooms is the room store that holds the delivery rows.
func (s *Store) deliveryRooms() *room.Store {
	return &room.Store{Root: filepath.Join(filepath.Dir(s.root()), "rooms")}
}

// sessionClaims reads every delivery row of one session in its room, which is
// what the whole queue blob held before.
func sessionClaims(rooms *room.Store, meta Meta) (deliveryClaims, error) {
	return claimsRows(rooms, meta.RoomID, meta.ID, meta.Address())
}

// claimsRows reads the claim state of one session whose rows may be keyed by
// its ID or by the address it answers to (see room.DeliveryKeys).
func claimsRows(rooms *room.Store, roomID, sessionID, addr string) (deliveryClaims, error) {
	if rooms == nil {
		return claimsFromList(nil), nil
	}
	rows, err := rooms.SessionDeliveryRows(roomID, room.DeliveryKeys(sessionID, addr))
	if err != nil {
		return deliveryClaims{}, err
	}
	return claimsFromList(rows), nil
}

// postClaims reads the claim state of one session for the named post IDs.
func postClaims(rooms *room.Store, meta Meta, postIDs []string) (deliveryClaims, error) {
	rows, err := inputRows(rooms, meta.RoomID, meta.ID, meta.Address(), postIDs)
	if err != nil {
		return deliveryClaims{}, err
	}
	return claimsFromRows(rows), nil
}

// claimPosts gives the deliveries of postIDs to turn, and reports which of
// them the rows now hold. The session change keeps the app's view of the
// queue current.
func (s *Store) claimPosts(ctx context.Context, rooms *room.Store, meta Meta, postIDs []string, turn int) ([]string, error) {
	if len(postIDs) == 0 {
		return nil, nil
	}
	return rooms.ClaimDeliveries(ctx, sessionlife.Changed(meta.ID), deliverySet(meta), postIDs, turn)
}

// cancelPost cancels one queued delivery of this session. It reports whether
// it cancelled one: a post a turn already claimed is not cancelled.
func (s *Store) cancelPost(ctx context.Context, rooms *room.Store, meta Meta, postID string) (bool, error) {
	return rooms.CancelDelivery(ctx, sessionlife.Changed(meta.ID), deliverySet(meta), postID)
}

// dismissPost dismisses one pending delivery of this session.
func (s *Store) dismissPost(ctx context.Context, rooms *room.Store, meta Meta, postID string) (bool, error) {
	return rooms.DismissDelivery(ctx, sessionlife.Changed(meta.ID), deliverySet(meta), postID)
}

// markPostsDelivered records that the session received postIDs. The turn
// records stay the proof of delivery; this keeps the row's state as true.
func (s *Store) markPostsDelivered(ctx context.Context, rooms *room.Store, meta Meta, postIDs []string) error {
	_, err := rooms.MarkDelivered(ctx, deliverySet(meta), postIDs)
	return err
}

// deliverySignature is the newest write of this session's delivery rows, which
// changes when a claim, cancellation or dismissal does.
func (s *Store) deliverySignature(meta Meta) string {
	signature, err := s.deliveryRooms().DeliverySignature(meta.RoomID, room.DeliveryKeys(meta.ID, meta.Address()))
	if err != nil || signature == 0 {
		return ""
	}
	return "q" + strconv.FormatInt(signature, 10)
}

// storeClaimReply commits one input read's claim effects and its exact reply in
// the same transaction: the posts the read claimed, the notes a held prompt
// covered, and the reply the request receipt keeps. A retry reads the same
// rows and replays the same receipt.
func storeClaimReply(ctx context.Context, rooms *room.Store, meta Meta, claimed, covered []string, turn int, reply []byte, notes map[room.DeliveryHow][]string) error {
	ev := hub.Event{}
	if len(claimed) > 0 || len(covered) > 0 {
		ev = sessionlife.Changed(meta.ID)
	}
	db, err := database.Open(filepath.Dir(rooms.Root))
	if err != nil {
		return err
	}
	at := time.Now().UnixMilli()
	return rooms.DeliveryWrite(ctx, ev, func(conn *sql.Conn) error {
		if _, err := room.ClaimDeliveriesIn(ctx, conn, deliverySet(meta), claimed, turn); err != nil {
			return err
		}
		if _, err := room.CoverDeliveriesIn(ctx, conn, deliverySet(meta), covered); err != nil {
			return err
		}
		// how and delivered_at land with the claim, so a crash cannot leave a
		// claimed row with no outcome.
		for how, ids := range notes {
			if err := room.NoteDeliveriesIn(ctx, conn, noteSet(meta), ids, how, turn, at); err != nil {
				return err
			}
		}
		return state.For(db).FinishRequestIn(ctx, conn, "RunnerClaimInput", reply)
	})
}

// markTurnDelivered records the row state of the posts a turn delivered. The
// turn's own steering records stay the proof of delivery; this keeps the
// delivery row's state true, so a later reader sees how far each delivery got.
func (m *Manager) markTurnDelivered(ctx context.Context, meta Meta, turn Turn) {
	var delivered []string
	for _, steer := range turn.Steering {
		if steer.Delivered && steer.ID != "" {
			delivered = append(delivered, steer.ID)
		}
	}
	if len(delivered) == 0 {
		return
	}
	if err := m.store().markPostsDelivered(ctx, m.rooms(), meta, delivered); err != nil {
		log.Printf("session %s: mark delivered posts: %v", meta.ID, err)
	}
}
