package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"

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

// recordKey and seatKey name a room's rollback copies; roomKey is the stem
// both lie under.
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

// readRecord returns room id's row, after importing a room.json or
// seat.json that an earlier release wrote. The check takes no lock, so a
// room whose files are our own copies costs two stats and two stamp reads;
// only a foreign file takes the room lock to import. Callers that hold the
// room lock use readRecordHeld.
func (s *Store) readRecord(id string) (Record, error) {
	db, err := s.recordsDB()
	if err != nil {
		return Record{}, err
	}
	if err := s.importForeign(db, id, false); err != nil {
		return Record{}, err
	}
	return storedRecord(db, id)
}

// readRecordHeld is readRecord for a caller that holds the room lock.
func (s *Store) readRecordHeld(id string) (Record, error) {
	db, err := s.recordsDB()
	if err != nil {
		return Record{}, err
	}
	if err := s.importForeign(db, id, true); err != nil {
		return Record{}, err
	}
	return storedRecord(db, id)
}

// importForeign imports room id's room.json and seat.json when an earlier
// release wrote them. held says the caller holds the room lock; otherwise
// it is taken only when a file is foreign.
//
// TODO: legacy/shim remove later — the earlier release reads and writes room.json and seat.json only; a file it wrote is imported first, on read, in place of an import walk at engine start. Remove with item 31, when no binary from before this release is alive.
func (s *Store) importForeign(db *sql.DB, id string, held bool) error {
	dir, err := s.dir(id)
	if err != nil {
		return err
	}
	w, ctx := state.For(db), context.Background()
	recordPath, seatPath := filepath.Join(dir, "room.json"), filepath.Join(dir, "seat.json")
	if !held {
		foreign := false
		for _, c := range []struct{ key, path string }{{recordKey(id), recordPath}, {seatKey(id), seatPath}} {
			f, err := w.Foreign(ctx, c.key, c.path)
			if err != nil {
				return err
			}
			foreign = foreign || f
		}
		if !foreign {
			return nil
		}
		return s.withRoomLock(id, func() error { return s.importForeign(db, id, true) })
	}
	legacySeat := false
	if _, err := w.ImportIfForeign(ctx, recordKey(id), recordPath, func(conn *sql.Conn, raw []byte) error {
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("room %s: room.json: %v: %w", id, err, state.ErrSkipForeign)
		}
		if rec.ID != id {
			return fmt.Errorf("room %s: room.json names room %q: %w", id, rec.ID, state.ErrSkipForeign)
		}
		if err := putRecord(ctx, conn, rec); err != nil {
			return err
		}
		// TODO: legacy/shim remove later — #540 to #594 saved an attached seat in room.json; it becomes the seat row when the room has no seat.json, and seat.json is written for the earlier release below. Remove once every room with an attached seat has seat.json.
		var legacy struct {
			Seat Seat `json:"seat"`
		}
		if json.Unmarshal(raw, &legacy) != nil || legacy.Seat == (Seat{}) {
			return nil
		}
		if _, err := os.Lstat(seatPath); !os.IsNotExist(err) {
			return err
		}
		var has bool
		if err := conn.QueryRowContext(ctx, "SELECT seat IS NOT NULL FROM room_records WHERE id = ?", id).Scan(&has); err != nil || has {
			return err
		}
		legacySeat = true
		return putSeat(ctx, conn, id, legacy.Seat)
	}); err != nil {
		return err
	}
	if _, err := w.ImportIfForeign(ctx, seatKey(id), seatPath, func(conn *sql.Conn, raw []byte) error {
		var seat Seat
		if err := json.Unmarshal(raw, &seat); err != nil {
			return fmt.Errorf("room %s: seat.json: %v: %w", id, err, state.ErrSkipForeign)
		}
		return putSeat(ctx, conn, id, seat)
	}); err != nil {
		return err
	}
	if legacySeat {
		rec, err := storedRecord(db, id)
		if err != nil {
			return err
		}
		s.mirrorSeat(w, rec)
	}
	return nil
}

// writeRecord stores rec as its room's row. The caller holds the room lock
// (or the root lock for a new room) and read rec with readRecordHeld, which
// imported any earlier release's file first. mirror writes room.json for
// the earlier release after the commit.
func (s *Store) writeRecord(rec Record, mirror bool) error {
	db, err := s.recordsDB()
	if err != nil {
		return err
	}
	w := state.For(db)
	if err := w.Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putRecord(context.Background(), conn, rec)
	}); err != nil {
		return err
	}
	if mirror {
		s.mirrorRecord(w, rec)
	}
	return nil
}

// touchRecord records activity on rec, which also revives a manually
// archived room.
//
// TODO: legacy/shim remove later — room.json is copied only when the
// earlier release acts on the change (ManualArchive), not for every post's
// activity time. After a rollback the earlier release sees a stale last
// activity until the room's next mirrored change, a named and harmless loss
// under item 24; it may show an active room as archived. Remove with item 31.
func (s *Store) touchRecord(rec Record) error {
	mirror := rec.ManualArchive
	rec.LastActivity = s.now()
	rec.ManualArchive = false
	return s.writeRecord(rec, mirror)
}

// writeSeat stores seat as room id's seat row and copies seat.json for the
// earlier release. The caller holds the room lock.
func (s *Store) writeSeat(id string, seat Seat) error {
	db, err := s.recordsDB()
	if err != nil {
		return err
	}
	w := state.For(db)
	if err := w.Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putSeat(context.Background(), conn, id, seat)
	}); err != nil {
		return err
	}
	rec, err := storedRecord(db, id)
	if err != nil {
		return err
	}
	s.mirrorSeat(w, rec)
	return nil
}

// TODO: legacy/shim remove later — the earlier release reads a room only from room.json and its seat only from seat.json, so a downgrade still sees every room; remove with item 31.
func (s *Store) mirrorRecord(w *state.Writer, rec Record) {
	s.mirror(w, rec.ID, recordKey(rec.ID), "room.json", rec)
}

// TODO: legacy/shim remove later — see mirrorRecord.
func (s *Store) mirrorSeat(w *state.Writer, rec Record) {
	s.mirror(w, rec.ID, seatKey(rec.ID), "seat.json", rec.Seat)
}

// mirror writes v as file name in room id's folder. A failed copy is
// logged: the row is committed.
func (s *Store) mirror(w *state.Writer, id, key, name string, v any) error {
	dir, err := s.dir(id)
	if err == nil {
		// The earlier release's format: indented, with a final newline.
		err = w.Mirror(context.Background(), key, filepath.Join(dir, name), func(out io.Writer) error {
			b, err := json.MarshalIndent(v, "", "  ")
			if err == nil {
				_, err = out.Write(append(b, '\n'))
			}
			return err
		})
	}
	if err != nil {
		log.Printf("room %s: %s copy for the earlier release: %v", id, name, err)
	}
	return err
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

// deleteRows removes room id's record, issue, pin and owner-read rows and the
// stamps of its rollback copies in one write, and raises rooms.revision.
func (s *Store) deleteRows(id string) error {
	if _, err := s.issuesDB(); err != nil {
		return err
	}
	if _, err := s.pinsDB(); err != nil {
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
		} {
			if _, err := conn.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		if err := invalidateOwnerReadMirror(ctx, conn, id); err != nil {
			return err
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
	err = database.Migrate(db, "room.owner_reads", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), ownerReadsSchema)
		return err
	})
	return db, err
}

func ownerReadsKey(id string) string { return roomKey(id) + "/owner-read" }

// TODO: legacy/shim remove later — records which owner-read.json copy includes all canonical marks; remove with that mirror.
func ownerReadsMirrorKey(id string) string { return ownerReadsKey(id) + "/complete" }

func invalidateOwnerReadMirror(ctx context.Context, conn *sql.Conn, roomID string) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM meta WHERE key=?", ownerReadsMirrorKey(roomID))
	return err
}

// importOwnerReads imports room id's owner-read.json when the earlier
// release wrote it.
//
// TODO: legacy/shim remove later — the earlier release reads and writes only owner-read.json; remove with item 31.
func (s *Store) importOwnerReads(db *sql.DB, roomID string, held bool) error {
	dir, err := s.dir(roomID)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ownerReadsFile)
	w := state.For(db)
	if foreign, err := w.Foreign(context.Background(), ownerReadsKey(roomID), path); err != nil || !foreign {
		return err
	}
	read := func() error {
		_, err := w.ImportIfForeign(context.Background(), ownerReadsKey(roomID), path, func(conn *sql.Conn, raw []byte) error {
			var ids []string
			if err := json.Unmarshal(raw, &ids); err != nil {
				return fmt.Errorf("room %s: %s: %v: %w", roomID, ownerReadsFile, err, state.ErrSkipForeign)
			}
			// The earlier release only adds reads, so the file's list is
			// a superset of what it saw: add, never remove.
			if _, err := putOwnerReads(context.Background(), conn, roomID, ids); err != nil {
				return err
			}
			// Even an imported subset can omit newer canonical marks.
			return invalidateOwnerReadMirror(context.Background(), conn, roomID)
		})
		return err
	}
	if held {
		return read()
	}
	return s.withRoomLock(roomID, read)
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

// Tests count the real full-row materialization before sorting and mirroring.
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

func sortedIDs(ids map[string]bool) []string {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	return list
}
