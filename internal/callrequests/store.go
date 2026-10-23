// Package callrequests stores owner call requests in the shared SQLite database.
package callrequests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/state"
)

// Topic identifies committed call request list changes.
const Topic = "call-requests"

var errUnchanged = errors.New("call request unchanged")

type Request struct {
	ID             string     `json:"id"`
	Room           string     `json:"room"`
	Reason         string     `json:"reason"`
	Asker          string     `json:"asker"`
	State          string     `json:"state"`
	ClosedBy       string     `json:"closedBy"`
	AskedAt        time.Time  `json:"askedAt"`
	ShownAt        *time.Time `json:"shownAt,omitempty"`
	PushSentAt     *time.Time `json:"pushSentAt,omitempty"`
	ClosedAt       *time.Time `json:"closedAt,omitempty"`
	CloseDelivered bool       `json:"closeDelivered"`
}

type Store struct{ Root string }

const schema = `CREATE TABLE call_requests (
 room TEXT NOT NULL, id TEXT NOT NULL, reason TEXT NOT NULL, asker TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('ringing','joined','declined','withdrawn')),
 closed_by TEXT NOT NULL DEFAULT '', asked_at INTEGER NOT NULL,
 shown_at INTEGER, push_sent_at INTEGER, closed_at INTEGER,
 close_delivered INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(room,id)
);
CREATE UNIQUE INDEX call_requests_ringing ON call_requests(room) WHERE state='ringing';
CREATE TABLE call_request_next (room TEXT PRIMARY KEY, next INTEGER NOT NULL);`

// Migrate brings the call request tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	_, err := (&Store{Root: dataDir}).open()
	return err
}

func (s *Store) open() (*sql.DB, error) {
	db, err := database.Open(s.Root)
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "callrequests.schema", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `CREATE TABLE call_requests_revision (
   id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL
  ); INSERT INTO call_requests_revision VALUES(1,0);`)
		return err
	})
	return db, err
}

const columns = `id,room,reason,asker,state,closed_by,asked_at,shown_at,push_sent_at,closed_at,close_delivered`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Request, error) {
	var r Request
	var asked int64
	var shown, pushed, closed sql.NullInt64
	err := row.Scan(&r.ID, &r.Room, &r.Reason, &r.Asker, &r.State, &r.ClosedBy, &asked, &shown, &pushed, &closed, &r.CloseDelivered)
	if err != nil {
		return r, err
	}
	r.AskedAt = time.Unix(0, asked).UTC()
	r.ShownAt, r.PushSentAt, r.ClosedAt = timestamp(shown), timestamp(pushed), timestamp(closed)
	return r, nil
}

func timestamp(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := time.Unix(0, value.Int64).UTC()
	return &t
}

func get(conn *sql.Conn, room, id string) (Request, error) {
	return scan(conn.QueryRowContext(context.Background(), "SELECT "+columns+" FROM call_requests WHERE room=? AND id=?", room, id))
}

// setChange encodes the committed row in the shared list-event shape.
func setChange(conn *sql.Conn, event *listchange.Event, row Request) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	event.Change = listchange.Change{Op: "upsert", ID: row.ID, Item: raw}
	_, err = conn.ExecContext(context.Background(), "UPDATE call_requests_revision SET revision=revision+1 WHERE id=1")
	return err
}

// Ask creates a request or updates the reason of the room's ringing request.
func (s *Store) Ask(room, reason, asker string) (Request, bool, error) {
	if strings.TrimSpace(room) == "" || strings.TrimSpace(reason) == "" || strings.TrimSpace(asker) == "" {
		return Request{}, false, fmt.Errorf("a call request needs a room, reason and asker")
	}
	db, err := s.open()
	if err != nil {
		return Request{}, false, err
	}
	var r Request
	created := false
	event := listchange.Event{Kind: Topic, RoomID: room}
	err = state.For(db).Write(context.Background(), hub.Event{Topic: Topic, Room: room, Body: &event}, func(conn *sql.Conn) error {
		ctx := context.Background()
		var err error
		r, err = scan(conn.QueryRowContext(ctx, "SELECT "+columns+" FROM call_requests WHERE room=? AND state='ringing'", room))
		if err == nil {
			if r.Reason == reason {
				return errUnchanged
			}
			if _, err := conn.ExecContext(ctx, "UPDATE call_requests SET reason=? WHERE room=? AND id=?", reason, room, r.ID); err != nil {
				return err
			}
			r.Reason = reason
			return setChange(conn, &event, r)
		}
		if err != sql.ErrNoRows {
			return err
		}
		var next int64
		err = conn.QueryRowContext(ctx, "INSERT INTO call_request_next(room,next) VALUES(?,1) ON CONFLICT(room) DO UPDATE SET next=next+1 RETURNING next", room).Scan(&next)
		if err != nil {
			return err
		}
		r = Request{ID: fmt.Sprintf("c%d", next), Room: room, Reason: reason, Asker: asker, State: "ringing", AskedAt: time.Now().UTC()}
		_, err = conn.ExecContext(ctx, "INSERT INTO call_requests(room,id,reason,asker,state,asked_at) VALUES(?,?,?,?,'ringing',?)", room, r.ID, reason, asker, r.AskedAt.UnixNano())
		if err != nil {
			return err
		}
		created = true
		return setChange(conn, &event, r)
	})
	if errors.Is(err, errUnchanged) {
		err = nil
	}
	return r, created && err == nil, err
}

// Revision changes with each committed row change, including writes from
// another process. No-op requests leave it unchanged.
func (s *Store) Revision() (int64, error) {
	db, err := s.open()
	if err != nil {
		return 0, err
	}
	var revision int64
	err = db.QueryRow("SELECT revision FROM call_requests_revision WHERE id=1").Scan(&revision)
	return revision, err
}

// List returns history for one room, or all rooms when no room is supplied.
func (s *Store) List(room ...string) ([]Request, error) {
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	query := "SELECT " + columns + " FROM call_requests"
	var args []any
	if len(room) > 0 && room[0] != "" {
		query += " WHERE room=?"
		args = append(args, room[0])
	}
	rows, err := db.Query(query+" ORDER BY asked_at,room,id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Open(room string) (Request, error) {
	db, err := s.open()
	if err != nil {
		return Request{}, err
	}
	return scan(db.QueryRow("SELECT "+columns+" FROM call_requests WHERE room=? AND state='ringing'", room))
}

func (s *Store) Get(room, id string) (Request, error) {
	db, err := s.open()
	if err != nil {
		return Request{}, err
	}
	return scan(db.QueryRow("SELECT "+columns+" FROM call_requests WHERE room=? AND id=?", room, id))
}

func (s *Store) update(room, id, assignment, condition string, args ...any) (Request, bool, error) {
	db, err := s.open()
	if err != nil {
		return Request{}, false, err
	}
	var r Request
	changed := false
	event := listchange.Event{Kind: Topic, RoomID: room}
	err = state.For(db).Write(context.Background(), hub.Event{Topic: Topic, Room: room, Key: id, Body: &event}, func(conn *sql.Conn) error {
		params := append(args, room, id)
		result, err := conn.ExecContext(context.Background(), "UPDATE call_requests SET "+assignment+" WHERE room=? AND id=? AND "+condition, params...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = n > 0
		r, err = get(conn, room, id)
		if err != nil {
			return err
		}
		if !changed {
			return errUnchanged
		}
		return setChange(conn, &event, r)
	})
	if errors.Is(err, errUnchanged) {
		err = nil
	}
	return r, changed && err == nil, err
}

func (s *Store) Shown(room, id string) (Request, error) {
	r, _, err := s.update(room, id, "shown_at=?", "state='ringing' AND shown_at IS NULL", time.Now().UnixNano())
	return r, err
}

func (s *Store) ClaimPush(room, id string) (Request, bool, error) {
	return s.update(room, id, "push_sent_at=?", "state='ringing' AND shown_at IS NULL AND push_sent_at IS NULL", time.Now().UnixNano())
}

func (s *Store) Close(room, id, state, by string) (Request, bool, error) {
	if state != "joined" && state != "declined" && state != "withdrawn" {
		return Request{}, false, fmt.Errorf("invalid call request close state %q", state)
	}
	return s.update(room, id, "state=?,closed_by=?,closed_at=?", "state='ringing'", state, by, time.Now().UnixNano())
}

func (s *Store) MarkCloseDelivered(room, id string) error {
	_, _, err := s.update(room, id, "close_delivered=1", "state!='ringing' AND close_delivered=0")
	return err
}
