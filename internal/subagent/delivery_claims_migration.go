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

// importQueueClaims moves one blob's state into its delivery rows.
func importQueueClaims(ctx context.Context, conn *sql.Conn, stores *Store, blob storedQueueBlob) error {
	var queue legacyQueueClaims
	if err := json.Unmarshal(blob.raw, &queue); err != nil {
		return fmt.Errorf("read retired queue of session %s: %w", blob.session, err)
	}
	keys := room.DeliveryKeys(blob.session, "")
	if meta, err := stores.ReadMeta(blob.session); err == nil {
		keys = room.DeliveryKeys(blob.session, meta.Address())
	}
	for postID, turn := range queue.Claimed {
		if turn <= 0 {
			continue
		}
		if err := importState(ctx, conn, keys, postID, room.DeliveryClaimed, turn); err != nil {
			return err
		}
	}
	for postID := range queue.Cancelled {
		if err := importState(ctx, conn, keys, postID, room.DeliveryCancelled, 0); err != nil {
			return err
		}
	}
	for postID := range queue.Dismissed {
		if err := importState(ctx, conn, keys, postID, room.DeliveryDismissed, 0); err != nil {
			return err
		}
	}
	return nil
}

// importState sets the state of the delivery rows of postID named by keys, and
// the turn that holds it, which is 0 for a state no turn holds.
func importState(ctx context.Context, conn *sql.Conn, keys []string, postID string, state room.DeliveryState, turn int) error {
	var newest sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT MAX(updated_at) FROM post_deliveries").Scan(&newest); err != nil {
		return err
	}
	args := []any{string(state), turn, newest.Int64 + 1, postID}
	for _, key := range keys {
		args = append(args, key)
	}
	_, err := conn.ExecContext(ctx, "UPDATE post_deliveries SET state=?, claimed_turn=?, updated_at=? WHERE post_id=? AND session_id IN ("+inClause(keys)+")", args...)
	return err
}

// inClause lists keys for one IN clause: "?" for one key, "?,?" for two.
func inClause(keys []string) string {
	return "?" + strings.Repeat(",?", len(keys)-1)
}
