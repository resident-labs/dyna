package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
)

// deliveryClaimsImportKey records that every session's claim state moved from
// the session_queues blob into its delivery rows.
const deliveryClaimsImportKey = "subagent.delivery_claims.import"

// legacyQueueClaims is the shape of the retired session_queues blob, kept so
// migrateDeliveryClaims can read stores that still hold one.
type legacyQueueClaims struct {
	Claimed   map[string]int  `json:"claimed"`
	Cancelled map[string]bool `json:"cancelled"`
	Dismissed map[string]bool `json:"dismissed,omitempty"`
}

// TODO: legacy/shim remove later — with the session_queues table, once every
// supported store has run subagent.delivery_claims.import and no binary from
// before the delivery rows is alive.
//
// migrateDeliveryClaims copies each stored queue blob's claim, cancellation and
// dismissal into the matching delivery rows, once, then nothing reads or writes
// the blob again. A row is matched by post ID and by the session_id keys that
// name the session, so a row written for an address the session answers to is
// found too (see room.DeliveryKeys).
//
// The blob is the authority for this one step, so it overwrites the row's own
// state, in the blob's own order of strength: a post the blob claimed and then
// cancelled ends cancelled. The whole pass is one transaction, so a crash
// imports nothing and a retry imports it all.
func migrateDeliveryClaims(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	var done int
	err = db.QueryRow("SELECT 1 FROM meta WHERE key=?", deliveryClaimsImportKey).Scan(&done)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	indexed, err := deliveriesExist(db)
	if err != nil || !indexed {
		// A store that never ran room's post index has no delivery rows and so
		// no claim state to move.
		return err
	}
	blobs, err := storedQueueBlobs(db)
	if err != nil {
		return err
	}
	stores := &Store{Root: filepath.Join(dataDir, "sessions")}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		for _, blob := range blobs {
			if err := importQueueClaims(ctx, conn, stores, blob); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value=excluded.value", deliveryClaimsImportKey)
		return err
	})
}

// storedQueueBlob is one row of the retired queue table.
type storedQueueBlob struct {
	session string
	raw     []byte
}

// storedQueueBlobs reads every stored queue blob.
func storedQueueBlobs(db *sql.DB) ([]storedQueueBlob, error) {
	rows, err := db.Query("SELECT session, queue FROM session_queues")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedQueueBlob
	for rows.Next() {
		var blob storedQueueBlob
		if err := rows.Scan(&blob.session, &blob.raw); err != nil {
			return nil, err
		}
		out = append(out, blob)
	}
	return out, rows.Err()
}

// deliveriesExist reports whether the post index is in this store.
func deliveriesExist(db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='post_deliveries'").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// importQueueClaims moves one blob's state into its delivery rows. A blob also
// holds the state of posts this session never had a row for, because it
// inherited them from another inbox; those get a row, or the claim or dismissal
// would be lost and the post could reach the session again.
func importQueueClaims(ctx context.Context, conn *sql.Conn, stores *Store, blob storedQueueBlob) error {
	var queue legacyQueueClaims
	if err := json.Unmarshal(blob.raw, &queue); err != nil {
		return fmt.Errorf("read retired queue of session %s: %w", blob.session, err)
	}
	keys := room.DeliveryKeys(blob.session, "")
	roomID := ""
	if meta, err := stores.ReadMeta(blob.session); err == nil {
		keys = room.DeliveryKeys(blob.session, meta.Address())
		roomID = meta.RoomID
	}
	target := importTarget{roomID: roomID, keys: keys}
	for postID, turn := range queue.Claimed {
		if turn <= 0 {
			continue
		}
		if err := importState(ctx, conn, target, postID, room.DeliveryClaimed, turn); err != nil {
			return err
		}
	}
	for postID := range queue.Cancelled {
		if err := importState(ctx, conn, target, postID, room.DeliveryCancelled, 0); err != nil {
			return err
		}
	}
	for postID := range queue.Dismissed {
		if err := importState(ctx, conn, target, postID, room.DeliveryDismissed, 0); err != nil {
			return err
		}
	}
	return nil
}

// importTarget names the rows one blob's state belongs on: the room the session
// is in, empty when its record is gone, and the session_id keys that name it.
type importTarget struct {
	roomID string
	keys   []string
}

// importState gives the deliveries of postID the blob's state, and the turn that
// holds it, which is 0 for a state no turn holds. A post the session has no row
// for gets one, which is how an inherited claim or dismissal survives, and that
// row copies the post's delivery mode, so the wake decision does not change.
func importState(ctx context.Context, conn *sql.Conn, target importTarget, postID string, state room.DeliveryState, turn int) error {
	var newest sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT MAX(updated_at) FROM post_deliveries").Scan(&newest); err != nil {
		return err
	}
	at := newest.Int64 + 1
	args := []any{string(state), turn, at, postID}
	for _, key := range target.keys {
		args = append(args, key)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE post_deliveries SET state=?, claimed_turn=?, updated_at=? WHERE post_id=? AND session_id IN ("+inClause(target.keys)+")", args...); err != nil {
		return err
	}
	if target.roomID == "" {
		return nil
	}
	// Only a post the session has no row for gets one: a row it already has,
	// keyed by its ID or by its address, holds the state the update above set.
	countArgs := []any{target.roomID, postID}
	for _, key := range target.keys {
		countArgs = append(countArgs, key)
	}
	var rows int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM post_deliveries WHERE room_id=? AND post_id=? AND session_id IN ("+inClause(target.keys)+")", countArgs...).Scan(&rows); err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	var mode string
	err := conn.QueryRowContext(ctx, `SELECT mode FROM post_deliveries WHERE room_id=? AND post_id=?
ORDER BY mode='wake' DESC LIMIT 1`, target.roomID, postID).Scan(&mode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		mode = string(room.DeliveryQuiet)
	}
	_, err = conn.ExecContext(ctx, `INSERT OR IGNORE INTO post_deliveries(room_id, post_id, session_id, mode, category, state, claimed_turn, updated_at)
VALUES(?, ?, ?, ?, '', ?, ?, ?)`, target.roomID, postID, target.keys[0], mode, string(state), turn, at)
	return err
}

// inClause lists keys for one IN clause: "?" for one key, "?,?" for two.
func inClause(keys []string) string {
	return "?" + strings.Repeat(",?", len(keys)-1)
}
