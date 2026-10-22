package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// MaxPins is how many posts one room may keep pinned.
const MaxPins = 50

// Pin records that a post is kept near the top of the room, newest first.
type Pin struct {
	ID string `json:"id"`
	// By is the address of the member who pinned it. The owner is the room name.
	By string `json:"by"`
	At int64  `json:"at"` // unix ms
}

// PinsFile names the source imported by room.pins version 2.
const PinsFile = "pins.json"

// Pins live in dyna.db (rule r10, requirement 19), one row per pinned post.
// seq orders a room's pins, newest highest. room_pin_revisions counts each
// room's pin changes and never goes back, so the room watch sees an unpin.
const pinsSchema = `CREATE TABLE room_pins (
 room_id TEXT NOT NULL,
 msg_id TEXT NOT NULL,
 pinned_by TEXT NOT NULL,
 at INTEGER NOT NULL,
 seq INTEGER NOT NULL,
 PRIMARY KEY(room_id, msg_id)
);
CREATE TABLE room_pin_revisions (
 room_id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL
);`

func (s *Store) pinsDB() (*sql.DB, error) {
	db, err := s.recordsDB()
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.pins", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, pinsSchema); err != nil {
				return err
			}
		}
		return s.importPinFiles(ctx, conn)
	})
	return db, err
}

// importPinFiles is the one-time room.pins version 2 conversion. Original
// files stay unchanged. Existing pin rows or a revision, including an empty
// set after unpin, take precedence before any source file is inspected.
func (s *Store) importPinFiles(ctx context.Context, conn *sql.Conn) error {
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
		roomID := entry.Name()
		var canonical bool
		if err := conn.QueryRowContext(ctx, `SELECT
EXISTS(SELECT 1 FROM room_pins WHERE room_id=?) OR
EXISTS(SELECT 1 FROM room_pin_revisions WHERE room_id=?)`, roomID, roomID).Scan(&canonical); err != nil {
			return err
		}
		if canonical {
			continue
		}
		path := filepath.Join(s.root(), roomID, PinsFile)
		_, err := state.ImportMigration(ctx, conn, roomKey(roomID)+"/pins", path, func(conn *sql.Conn, raw []byte) error {
			var pins []Pin
			if err := json.Unmarshal(raw, &pins); err != nil {
				return fmt.Errorf("import room %s pins %s: %w", roomID, path, err)
			}
			return putPins(ctx, conn, roomID, pins)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// readPins returns room id's pins, newest first.
func (s *Store) readPins(roomID string) ([]Pin, error) {
	db, err := s.pinsDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT msg_id, pinned_by, at FROM room_pins WHERE room_id = ? ORDER BY seq DESC", roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pin
	for rows.Next() {
		var p Pin
		if err := rows.Scan(&p.ID, &p.By, &p.At); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// putPins replaces room id's pins with pins, given newest first, and counts
// the change.
func putPins(ctx context.Context, conn *sql.Conn, roomID string, pins []Pin) error {
	if _, err := conn.ExecContext(ctx, "DELETE FROM room_pins WHERE room_id = ?", roomID); err != nil {
		return err
	}
	for i, p := range pins {
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO room_pins(room_id, msg_id, pinned_by, at, seq) VALUES (?, ?, ?, ?, ?)", roomID, p.ID, p.By, p.At, len(pins)-i); err != nil {
			return err
		}
	}
	return bumpPins(ctx, conn, roomID)
}

func bumpPins(ctx context.Context, conn *sql.Conn, roomID string) error {
	_, err := conn.ExecContext(ctx, "INSERT INTO room_pin_revisions(room_id, revision) VALUES (?, 1) ON CONFLICT(room_id) DO UPDATE SET revision = revision + 1", roomID)
	return err
}

// writePins stores pins as room id's rows. The caller holds the room lock.
func (s *Store) writePins(roomID string, pins []Pin) error {
	db, err := s.pinsDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putPins(context.Background(), conn, roomID, pins)
	})
}

// Pins lists a room's pins, newest first. A room with none returns nil.
func (s *Store) Pins(roomID string) ([]Pin, error) {
	if _, err := s.dir(roomID); err != nil {
		return nil, err
	}
	return s.readPins(roomID)
}

// Pin keeps a post in the room's pinned list. It refuses a post the room
// does not hold. Pinning a pinned post changes nothing, so the first time
// and first pinner stay.
func (s *Store) Pin(roomID, msgID, by string) (Pin, error) {
	msgID, by = strings.TrimSpace(msgID), strings.TrimSpace(by)
	if msgID == "" || by == "" {
		return Pin{}, errors.New("a pin needs a post ID and a pinner")
	}
	var out Pin
	err := s.withRoomLock(roomID, func() error {
		pins, err := s.readPins(roomID)
		if err != nil {
			return err
		}
		for _, p := range pins {
			if p.ID == msgID {
				out = p
				return nil
			}
		}
		if _, err := s.Message(roomID, msgID); err != nil {
			return err
		}
		if len(pins) >= MaxPins {
			return fmt.Errorf("this room already has %d pinned posts; unpin one first", MaxPins)
		}
		out = Pin{ID: msgID, By: by, At: s.now().UnixMilli()}
		return s.writePins(roomID, append([]Pin{out}, pins...))
	})
	return out, err
}

// Unpin removes a pin. A post that is not pinned is not an error.
func (s *Store) Unpin(roomID, msgID string) error {
	msgID = strings.TrimSpace(msgID)
	return s.withRoomLock(roomID, func() error {
		pins, err := s.readPins(roomID)
		if err != nil {
			return err
		}
		kept := pins[:0:0]
		for _, p := range pins {
			if p.ID != msgID {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(pins) {
			return nil
		}
		return s.writePins(roomID, kept)
	})
}

// PinsRevision changes whenever the room's pins change, for change
// watchers. It is 0 for a room whose pins never changed.
func (s *Store) PinsRevision(roomID string) (int64, error) {
	db, err := s.pinsDB()
	if err != nil {
		return 0, err
	}
	var rev int64
	err = db.QueryRow("SELECT revision FROM room_pin_revisions WHERE room_id = ?", roomID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}
