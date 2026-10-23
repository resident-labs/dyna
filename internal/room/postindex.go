package room

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The post index is queryable runtime state, so it lives in dyna.db (rule
// r1). Post bodies stay in the room's JSONL log; these rows answer where a
// post was written and who received it, so a reader never works that out
// again from the address strings at read time.
//
// post_homes is an index, not a second copy of the post: ts and
// author_session_id let one query list a conversation or a session's own
// posts. post_deliveries holds one row per recipient and post. The cursor
// remembers how far the indexer reached in the room's log, so a crash between
// the log append and the row write leaves no post without rows.
const postIndexSchema = `CREATE TABLE post_homes (
 room_id TEXT NOT NULL,
 post_id TEXT NOT NULL,
 ts INTEGER NOT NULL,
 conversation TEXT NOT NULL,
 author_session_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(room_id, post_id)
);
CREATE INDEX post_homes_conversation ON post_homes(room_id, conversation, ts);
CREATE INDEX post_homes_author ON post_homes(author_session_id, ts);
CREATE TABLE post_deliveries (
 room_id TEXT NOT NULL,
 post_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('wake','quiet')),
 category TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('queued','claimed','delivered','cancelled','failed')),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(post_id, session_id)
);
CREATE INDEX post_deliveries_session ON post_deliveries(session_id, post_id);
CREATE TABLE post_index_cursor (
 room_id TEXT PRIMARY KEY,
 log_offset INTEGER NOT NULL
);`

// postIndexImportKey records that the retained posts of every room were
// imported into the post index.
const postIndexImportKey = "room.post_index.import"

// postIndexAuthorsKey records that the retained posts of every room were given
// the author their rows name today. It is a separate versioned key because the
// schema did not change: only the value one column gets for the seat's posts.
const postIndexAuthorsKey = "room.post_index.authors"

func (s *Store) postIndexDB() (*sql.DB, error) {
	db, err := s.recordsDB()
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "room.post_index", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), postIndexSchema)
		return err
	})
	return db, err
}

// migratePostIndex fills the post index at engine start: it imports the
// retained posts once, then catches up with whatever a crash left unindexed.
func (s *Store) migratePostIndex() error {
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	var version string
	err = db.QueryRow("SELECT value FROM meta WHERE key=?", postIndexImportKey).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := database.BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before importing the post index: %w", err)
		}
		if err := s.importPostIndex(db); err != nil {
			return err
		}
		// The import writes today's author, so this fresh index needs no
		// backfill of the authors it just stored.
		if err := markPostIndex(db, postIndexImportKey, postIndexAuthorsKey); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	if err := s.backfillPostAuthors(db); err != nil {
		return err
	}
	return s.catchUpPostIndex(db)
}

// markPostIndex records that these one-time steps of the post index ran.
func markPostIndex(db *sql.DB, keys ...string) error {
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		for _, key := range keys {
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO meta(key, value) VALUES(?, '1')
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key); err != nil {
				return err
			}
		}
		return nil
	})
}

// backfillPostAuthors gives the retained rows of an older store the author they
// would get today, once. Rows written before the seat named its own posts hold
// no author for an external root's post and the root session for the internal
// root's, so the seat's DM would lose them; INSERT OR IGNORE never repairs a
// row, so this rewrites it.
//
// It runs after the one-time import, and before the cursor catch-up, whose rows
// already carry today's author.
func (s *Store) backfillPostAuthors(db *sql.DB) error {
	var version string
	err := db.QueryRow("SELECT value FROM meta WHERE key=?", postIndexAuthorsKey).Scan(&version)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := database.BackUpOnce(db); err != nil {
		return fmt.Errorf("back up dyna.db before backfilling post authors: %w", err)
	}
	records, err := storedRecords(db)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if err := s.rewritePostAuthors(db, rec); err != nil {
			return err
		}
	}
	return markPostIndex(db, postIndexAuthorsKey)
}

// rewritePostAuthors rewrites the author of every retained post of one room
// whose row names another author than postAuthor does now, in one write per
// room. A post that names no author keeps its row as it is.
func (s *Store) rewritePostAuthors(db *sql.DB, rec Record) error {
	msgs, err := s.Messages(rec.ID)
	if err != nil {
		return err
	}
	type row struct {
		postID, author string
	}
	var rows []row
	for _, msg := range msgs {
		if author := postAuthor(msg, rec.Name, rec.ID); author != "" {
			rows = append(rows, row{postID: msg.ID, author: author})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		for _, r := range rows {
			if _, err := conn.ExecContext(ctx, `UPDATE post_homes SET author_session_id=? WHERE room_id=? AND post_id=? AND author_session_id<>?`,
				r.author, rec.ID, r.postID, r.author); err != nil {
				return err
			}
		}
		return nil
	})
}

// TODO: legacy/shim remove later — imports posts that were stored before the
// post index existed; remove once every supported store has run
// room.post_index.import.
//
// Imported deliveries are delivered: the readers that acted on them have
// already done so, and a later reader must not deliver them again.
func (s *Store) importPostIndex(db *sql.DB) error {
	records, err := storedRecords(db)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if err := s.indexPostsFrom(rec.ID, 0, DeliveryDelivered); err != nil {
			return err
		}
	}
	return nil
}

// catchUpPostIndex indexes the posts that a crash left without rows, from each
// room's stored cursor. Their deliveries are queued: nothing knows whether the
// post that crashed reached its recipients, so a later reader may deliver it
// again, and no post goes undelivered.
func (s *Store) catchUpPostIndex(db *sql.DB) error {
	records, err := storedRecords(db)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if err := s.indexPostsFrom(rec.ID, -1, DeliveryQueued); err != nil {
			return err
		}
	}
	return nil
}

// indexPostsFrom writes the rows of every post between offset and the end of
// the room's log, then moves the cursor past them. A negative offset starts at
// the room's stored cursor.
func (s *Store) indexPostsFrom(roomID string, offset int64, deliveryState DeliveryState) error {
	return s.withRoomLock(roomID, func() error {
		rec, err := s.readRecord(roomID)
		if err != nil {
			return err
		}
		db, err := s.postIndexDB()
		if err != nil {
			return err
		}
		if offset < 0 {
			if offset, err = storedOffsetCursor(db, roomID); err != nil {
				return err
			}
		}
		for {
			msgs, next, err := s.ReadFrom(roomID, offset)
			if err != nil {
				return err
			}
			if next == offset {
				return nil
			}
			if err := s.writePostIndex(db, rec.Name, roomID, msgs, next, deliveryState); err != nil {
				return err
			}
			offset = next
		}
	})
}

// storedOffsetCursor is the offset the indexer reached for roomID, or 0 when
// it never ran there.
func storedOffsetCursor(db *sql.DB, roomID string) (int64, error) {
	var offset int64
	err := db.QueryRow("SELECT log_offset FROM post_index_cursor WHERE room_id=?", roomID).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return offset, err
}

// writePostIndex stores the rows of msgs in one write and moves the cursor to
// next, the log offset just past their last line.
func (s *Store) writePostIndex(db *sql.DB, roomName, roomID string, msgs []Message, next int64, deliveryState DeliveryState) error {
	homes := s.homesFor(db, roomName, roomID, msgs)
	at := s.now().UnixMilli()
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		for i, msg := range msgs {
			if err := putPostRows(ctx, conn, roomID, roomName, msg, homes[i], deliveryState, at); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO post_index_cursor(room_id, log_offset) VALUES(?, ?)
ON CONFLICT(room_id) DO UPDATE SET log_offset = excluded.log_offset`, roomID, next)
		return err
	})
}

// homesFor resolves the home of every post in one log batch, in log order, so
// a post can look up the home of the post it replies to. It reads outside the
// write transaction, which keeps log reads out of the SQLite writer.
func (s *Store) homesFor(db *sql.DB, roomName, roomID string, msgs []Message) []Conversation {
	homes := make([]Conversation, len(msgs))
	batch := make(map[string]Conversation, len(msgs))
	for i, msg := range msgs {
		parent := Conversation("")
		if msg.ReplyTo != "" {
			if home, ok := batch[msg.ReplyTo]; ok {
				parent = home
			} else {
				parent = s.ReplyHome(db, roomID, msg.ReplyTo)
			}
		}
		homes[i] = HomeFor(msg, roomName, parent)
		batch[msg.ID] = homes[i]
	}
	return homes
}

// putPostRows stores one post's home row and its delivery rows. A row for the
// same post is kept as it is: a post's rows are written once.
func putPostRows(ctx context.Context, conn *sql.Conn, roomID, roomName string, msg Message, home Conversation, deliveryState DeliveryState, at int64) error {
	if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO post_homes(room_id, post_id, ts, conversation, author_session_id) VALUES(?, ?, ?, ?, ?)`,
		roomID, msg.ID, msg.TS, string(home.Home()), postAuthor(msg, roomName, roomID)); err != nil {
		return err
	}
	for _, delivery := range DeliveriesFor(msg, roomName) {
		if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO post_deliveries(room_id, post_id, session_id, mode, category, state, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			roomID, msg.ID, delivery.SessionID, string(delivery.Mode), delivery.Category, string(deliveryState), at); err != nil {
			return err
		}
	}
	return nil
}

// postAuthor is who wrote msg, as post_homes records it: the session bound to
// the sender's address, or the room's root seat for a post of the seat itself.
// The seat's holder can change, so its posts name the seat (see
// SeatSessionID), the same identity the seat's delivery rows use, and the
// seat's DM finds them whoever holds it now.
//
// An event is the engine's notice rather than the seat's own words, so it names
// no author. An owner, worker or engine post names none either.
func postAuthor(msg Message, roomName, roomID string) string {
	if msg.From == roomName+":"+RootAgent {
		return SeatSessionID(roomID)
	}
	if id := msg.AuthorSessionID(); id != "" {
		return id
	}
	if msg.From != roomName || msg.Human {
		return ""
	}
	if _, event := eventSpecFor(msg.Kind, msg.Notice.Kind); event {
		return ""
	}
	return SeatSessionID(roomID)
}

// ReplyHome is the home of the post that postID answers, and empty when the
// room has no such post. It reads the index first and falls back to the stored
// post, whose own conversation field older posts may not carry. A nil db skips
// the index, for a caller that could not open it.
//
// The fallback reads the field only, without the rules that decided it, so a
// reply to a post stored before the field existed and still missing its index
// row goes to the room feed. The one-time import gives every such post a row,
// so this only matters before it runs.
func (s *Store) ReplyHome(db *sql.DB, roomID, postID string) Conversation {
	if postID == "" {
		return ""
	}
	if db != nil {
		var home string
		err := db.QueryRow("SELECT conversation FROM post_homes WHERE room_id=? AND post_id=?", roomID, postID).Scan(&home)
		switch {
		case err == nil:
			return Conversation(home)
		case !errors.Is(err, sql.ErrNoRows):
			log.Printf("room %s: read post home of %s: %v", roomID, postID, err)
		}
	}
	parent, err := s.Message(roomID, postID)
	if err != nil {
		return ""
	}
	return parent.Conversation
}

// indexPostRows writes the home and delivery rows of a post the store is
// about to append. It runs before the append, so a reader that finds the post
// in the log also finds its rows: they are what decide who reads the post, and
// a reader that saw the line first would miss them. The store removes the rows
// again when the append fails (dropPostRows); a crash between the two steps
// leaves them behind, and a row reader skips a row whose post the log does not
// hold (see NewestSeatDelivery).
func (s *Store) indexPostRows(roomID, roomName string, msg Message) error {
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	parent := s.ReplyHome(db, roomID, msg.ReplyTo)
	at := s.now().UnixMilli()
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		return putPostRows(ctx, conn, roomID, roomName, msg, HomeFor(msg, roomName, parent), DeliveryQueued, at)
	})
}

// dropPostRows removes the index rows of a post the store could not append, so
// no reader acts on a delivery for a post the log does not hold. A failed
// removal leaves the row where it looks like the crash case: a reader skips a
// row whose post is not in the log.
func (s *Store) dropPostRows(roomID, postID string) {
	db, err := s.postIndexDB()
	if err != nil {
		log.Printf("room %s: post %s: index rows of a failed append are left in place: %v", roomID, postID, err)
		return
	}
	err = state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, "DELETE FROM post_deliveries WHERE room_id=? AND post_id=?", roomID, postID); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "DELETE FROM post_homes WHERE room_id=? AND post_id=?", roomID, postID)
		return err
	})
	if err != nil {
		log.Printf("room %s: post %s: index rows of a failed append are left in place: %v", roomID, postID, err)
	}
}

// advancePostIndex moves the cursor past the post the store just appended at
// logStart, so the catch-up at the next engine start leaves its rows alone.
func (s *Store) advancePostIndex(roomID string, logStart int64) error {
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	end, err := s.FileSize(roomID)
	if err != nil {
		return err
	}
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return advancePostCursor(context.Background(), conn, roomID, logStart, end)
	})
}

// advancePostCursor moves the cursor to end only when it had already reached
// the start of the post just written. Otherwise posts before this one are
// still unindexed, and the catch-up must find them.
func advancePostCursor(ctx context.Context, conn *sql.Conn, roomID string, postStart, end int64) error {
	var cursor int64
	err := conn.QueryRowContext(ctx, "SELECT log_offset FROM post_index_cursor WHERE room_id=?", roomID).Scan(&cursor)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No indexer pass yet: only an empty log proves this post is the first.
		if postStart != 0 {
			return nil
		}
	case err != nil:
		return err
	case cursor < postStart:
		return nil
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO post_index_cursor(room_id, log_offset) VALUES(?, ?)
ON CONFLICT(room_id) DO UPDATE SET log_offset = excluded.log_offset`, roomID, end)
	return err
}
