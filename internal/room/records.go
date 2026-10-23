package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// Room records live in dyna.db (rule r10, requirement 19), one row per room.
// body is the record's JSON without the seat; seat is the root seat's JSON,
// NULL while the seat never changed. rev is the value of the rooms.revision
// counter at the row's last write.
const recordsSchema = `CREATE TABLE room_records (
 id TEXT PRIMARY KEY,
 rev INTEGER NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body)),
 seat TEXT CHECK(seat IS NULL OR json_valid(seat))
);`

// RevisionKey counts room record writes and deletes in dyna.db's meta
// table. It never goes back, so a reader that saw one value misses no later
// change, deletes included.
const RevisionKey = "rooms.revision"

func (s *Store) recordsDB() (*sql.DB, error) {
	dataDir := filepath.Dir(s.root())
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.records", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), recordsSchema)
		return err
	})
	return db, err
}

// migrateRecords imports retained room files once at engine startup.
func (s *Store) migrateRecords() error {
	db, err := s.recordsDB()
	if err != nil {
		return err
	}
	var version string
	err = db.QueryRow("SELECT value FROM meta WHERE key='room.records.import'").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if err := database.BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before importing room records: %w", err)
		}
	} else if err != nil {
		return err
	}
	err = database.Migrate(db, "room.records.import", 1, func(conn *sql.Conn, _ int) error {
		return s.importRoomFiles(context.Background(), conn)
	})
	if err != nil {
		return err
	}
	return nil
}

// TODO: legacy/shim remove later — imports retained room.json and seat.json
// only during room.records.import; remove after supported stores have migrated.
func (s *Store) importRoomFiles(ctx context.Context, conn *sql.Conn) error {
	entries, err := os.ReadDir(s.root())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		id := entry.Name()
		// A canonical row owns both columns, including an unchanged or detached seat.
		var canonical bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM room_records WHERE id=?)", id).Scan(&canonical); err != nil {
			return err
		}
		if canonical {
			continue
		}
		recordPath := filepath.Join(s.root(), id, "room.json")
		seatPath := filepath.Join(s.root(), id, "seat.json")
		if _, err := state.ImportMigration(ctx, conn, recordKey(id), recordPath, func(conn *sql.Conn, raw []byte) error {
			var imported struct {
				Record
				Seat Seat `json:"seat"`
			}
			if err := json.Unmarshal(raw, &imported); err != nil {
				return fmt.Errorf("import room %s: %w", recordPath, err)
			}
			if imported.ID != id {
				return fmt.Errorf("import room %s: record ID does not match its directory", recordPath)
			}
			if err := putRecord(ctx, conn, imported.Record); err != nil {
				return err
			}
			if imported.Seat == (Seat{}) {
				return nil
			}
			if _, err := os.Lstat(seatPath); !os.IsNotExist(err) {
				return err
			}
			if err := putSeat(ctx, conn, id, imported.Seat); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := state.ImportMigration(ctx, conn, seatKey(id), seatPath, func(conn *sql.Conn, raw []byte) error {
			var seat Seat
			if err := json.Unmarshal(raw, &seat); err != nil {
				return fmt.Errorf("import room seat %s: %w", seatPath, err)
			}
			return putSeat(ctx, conn, id, seat)
		}); err != nil {
			return err
		}
	}
	return nil
}

// recordKey and seatKey identify retained import sources; roomKey is their stem.
func roomKey(id string) string   { return "rooms/" + id }
func recordKey(id string) string { return roomKey(id) + "/record" }
func seatKey(id string) string   { return roomKey(id) + "/seat" }

// nextRevision raises rooms.revision and returns it.
func nextRevision(ctx context.Context, conn *sql.Conn) (int64, error) {
	var n int64
	err := conn.QueryRowContext(ctx, `INSERT INTO meta(key, value) VALUES (?, '1')
ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1 RETURNING CAST(value AS INTEGER)`, RevisionKey).Scan(&n)
	return n, err
}

// putRecord stores rec's body as its room's row. The seat column is left
// alone: only putSeat writes it.
func putRecord(ctx context.Context, conn *sql.Conn, rec Record) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	rev, err := nextRevision(ctx, conn)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO room_records(id, rev, body) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET rev = excluded.rev, body = excluded.body`, rec.ID, rev, string(body))
	return err
}

func putSeat(ctx context.Context, conn *sql.Conn, id string, seat Seat) error {
	raw, err := json.Marshal(seat)
	if err != nil {
		return err
	}
	rev, err := nextRevision(ctx, conn)
	if err != nil {
		return err
	}
	res, err := conn.ExecContext(ctx, `UPDATE room_records SET rev = ?, seat = ? WHERE id = ?`, rev, string(raw), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err == nil {
			err = fmt.Errorf("%w %q", ErrNotFound, id)
		}
		return err
	}
	return nil
}

// scanRecord decodes a row's body and seat.
func scanRecord(body string, seat sql.NullString) (Record, error) {
	var rec Record
	if err := json.Unmarshal([]byte(body), &rec); err != nil {
		return Record{}, err
	}
	if seat.Valid {
		if err := json.Unmarshal([]byte(seat.String), &rec.Seat); err != nil {
			return Record{}, err
		}
	}
	return rec, nil
}

// storedRecord is room id's row.
func storedRecord(db *sql.DB, id string) (Record, error) {
	var body string
	var seat sql.NullString
	err := db.QueryRow("SELECT body, seat FROM room_records WHERE id = ?", id).Scan(&body, &seat)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w %q", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, err
	}
	rec, err := scanRecord(body, seat)
	if err != nil {
		return Record{}, fmt.Errorf("parse room %s: %w", id, err)
	}
	return rec, nil
}

// readRecord returns the canonical room row.
func (s *Store) readRecord(id string) (Record, error) {
	db, err := s.recordsDB()
	if err != nil {
		return Record{}, err
	}
	return storedRecord(db, id)
}

// writeRecord stores the room row. The caller holds the room or root lock.
func (s *Store) writeRecord(rec Record) error {
	db, err := s.recordsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putRecord(context.Background(), conn, rec)
	})
}

// touchRecord records activity and revives a manually archived room.
func (s *Store) touchRecord(rec Record) error {
	rec.LastActivity = s.now()
	rec.ManualArchive = false
	return s.writeRecord(rec)
}

// writeSeat stores the canonical seat row. The caller holds the room lock.
func (s *Store) writeSeat(id string, seat Seat) error {
	db, err := s.recordsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putSeat(context.Background(), conn, id, seat)
	})
}

// storedRecords is every room's row.
func storedRecords(db *sql.DB) ([]Record, error) {
	rows, err := db.Query("SELECT id, body, seat FROM room_records")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var id, body string
		var seat sql.NullString
		if err := rows.Scan(&id, &body, &seat); err != nil {
			return nil, err
		}
		rec, err := scanRecord(body, seat)
		if err != nil {
			log.Printf("room %s: parse row: %v", id, err)
			continue
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// deleteRows removes room id's record, issue, pin, owner-read and post-index
// rows and the stamps of its rollback copies in one write, and raises
// rooms.revision.
func (s *Store) deleteRows(id string) error {
	if _, err := s.postNormalizationsDB(); err != nil {
		return err
	}
	if _, err := s.issuesDB(); err != nil {
		return err
	}
	if _, err := s.pinsDB(); err != nil {
		return err
	}
	if _, err := s.postIndexDB(); err != nil {
		return err
	}
	db, err := s.ownerReadsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, "DELETE FROM room_records WHERE id = ?", id); err != nil {
			return err
		}
		if _, err := nextRevision(ctx, conn); err != nil {
			return err
		}
		if err := deleteIssueRows(ctx, conn, id); err != nil {
			return err
		}
		for _, q := range []string{
			"DELETE FROM room_pins WHERE room_id = ?",
			"DELETE FROM room_pin_revisions WHERE room_id = ?",
			"DELETE FROM room_owner_reads WHERE room_id = ?",
			"DELETE FROM room_post_normalizations WHERE room_id = ?",
			"DELETE FROM post_homes WHERE room_id = ?",
			"DELETE FROM post_deliveries WHERE room_id = ?",
			"DELETE FROM post_index_cursor WHERE room_id = ?",
		} {
			if _, err := conn.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return state.ForgetMirrors(ctx, conn, roomKey(id))
	})
}

// removeDir deletes a room's folder after its rows. It is its own function
// so the write ratchet can name it as not-state.
func removeDir(dir string) error { return os.RemoveAll(dir) }

// ownerReadsFile is the earlier release's list of posts the owner read.
const ownerReadsFile = "owner-read.json"

// The owner's read posts live in dyna.db, one row per post.
const ownerReadsSchema = `CREATE TABLE room_owner_reads (
 room_id TEXT NOT NULL,
 msg_id TEXT NOT NULL,
 PRIMARY KEY(room_id, msg_id)
) WITHOUT ROWID;`

func (s *Store) ownerReadsDB() (*sql.DB, error) {
	db, err := s.recordsDB()
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.owner_reads", 2, func(conn *sql.Conn, version int) error {
		if version == 0 {
			if _, err := conn.ExecContext(context.Background(), ownerReadsSchema); err != nil {
				return err
			}
		}
		return s.importRetainedOwnerReads(conn)
	})
	return db, err
}

// TODO: legacy/shim remove later — import retained owner-read.json once, without rewriting it.
func (s *Store) importRetainedOwnerReads(conn *sql.Conn) error {
	entries, err := os.ReadDir(s.root())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		var exists bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM room_owner_reads WHERE room_id=?)", entry.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		path := filepath.Join(s.root(), entry.Name(), ownerReadsFile)
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			log.Printf("room %s: skip malformed %s: %v", entry.Name(), path, err)
			continue
		}
		if _, err := putOwnerReads(ctx, conn, entry.Name(), ids); err != nil {
			return err
		}
	}
	return nil
}

func putOwnerReads(ctx context.Context, conn *sql.Conn, roomID string, ids []string) (bool, error) {
	added := false
	for _, id := range ids {
		result, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO room_owner_reads(room_id, msg_id) VALUES (?, ?)", roomID, id)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		added = added || n != 0
	}
	return added, nil
}

// Tests count full-set materialization to keep acknowledgements proportional to new marks.
var storedOwnerReads = func(db *sql.DB, roomID string) (map[string]bool, error) {
	rows, err := db.Query("SELECT msg_id FROM room_owner_reads WHERE room_id = ?", roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
