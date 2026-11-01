package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// RecordEvent appends one event in its own transaction.
func (s *Store) RecordEvent(ctx context.Context, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := s.AppendEvent(tx, event)
		return err
	})
}

// AppendEvent inserts an event in a transaction submitted through Write.
func (s *Store) AppendEvent(tx *sql.Tx, event contract.Event) (int64, error) {
	if tx != s.currentTx {
		return 0, errors.New("event transaction is not the active writer transaction")
	}
	body, err := json.Marshal(event.Body)
	if err != nil {
		return 0, err
	}
	var id string
	switch event.From.Kind {
	case contract.PrincipalSession:
		id = string(event.From.Session)
	case contract.PrincipalOwner:
		id = string(event.From.Device)
	}
	var room *string
	if event.Room != "" {
		value := string(event.Room)
		room = &value
	}
	// home is the session whose DM holds the event: a DM post, a turn that answered in a DM, or a stopped turn.
	var home *contract.SessionID
	var clientID *string
	switch body := event.Body.(type) {
	case contract.SessionRestarted:
		home = &body.Session
	case contract.PostCreated:
		if body.Home.Kind == contract.ConversationDM {
			home = &body.Home.Session
		}
		if body.ClientID != "" {
			clientID = &body.ClientID
		}
	case contract.TurnEnded:
		for _, reply := range body.Replies {
			if reply.Reply.Home.Kind == contract.ConversationDM {
				home = &reply.Reply.Home.Session
				break
			}
		}
		if home == nil && body.Outcome == contract.TurnStopped {
			home = &body.Session
		}
	}
	result, err := tx.Exec("INSERT INTO events(id,at,room,type,from_kind,from_id,body,home,client_id) VALUES(?,?,?,?,?,?,?,?,?)", event.ID, event.Time.UnixMilli(), room, event.Body.EventType(), event.From.Kind, id, body, home, clientID)
	if err != nil {
		return 0, err
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	event.Seq = seq
	event.Time = time.UnixMilli(event.Time.UnixMilli()).UTC()
	event.From = contract.Principal{Kind: event.From.Kind}
	switch event.From.Kind {
	case contract.PrincipalSession:
		event.From.Session = contract.SessionID(id)
	case contract.PrincipalOwner:
		event.From.Device = contract.DeviceID(id)
	}
	event.Body, err = contract.DecodeEventBody(event.Body.EventType(), body)
	if err != nil {
		return 0, err
	}
	s.pending = append(s.pending, event)
	return seq, nil
}

// Events returns matching events in sequence order.
func (s *Store) Events(ctx context.Context, room contract.RoomID, afterSeq int64, limit int, types []contract.EventType) ([]contract.Event, error) {
	return s.EventsPage(ctx, room, afterSeq, 0, limit, types, "")
}

// EventsPage returns one page after afterSeq, or before beforeSeq when it is set, in sequence order.
// A dm session keeps only the events whose home is that session's DM.
func (s *Store) EventsPage(ctx context.Context, room contract.RoomID, afterSeq, beforeSeq int64, limit int, types []contract.EventType, dm contract.SessionID) ([]contract.Event, error) {
	return s.eventsPage(ctx, room, afterSeq, beforeSeq, limit, types, dm, false)
}

// OwnerEvents pages the cross-room attention events.
func (s *Store) OwnerEvents(ctx context.Context, afterSeq int64, limit int) ([]contract.Event, error) {
	return s.eventsPage(ctx, "", afterSeq, 0, limit, []contract.EventType{"op.requested", "op.decided", "op.ran", "turn.started", "turn.ended", "session.started", "session.closed", "box.changed", "room.paused", "room.resumed"}, "", true)
}

func (s *Store) eventsPage(ctx context.Context, room contract.RoomID, afterSeq, beforeSeq int64, limit int, types []contract.EventType, dm contract.SessionID, allRooms bool) ([]contract.Event, error) {
	query := "SELECT seq,id,at,room,type,from_kind,from_id,body FROM events WHERE "
	// database/sql requires a heterogeneous variadic slice for query arguments.
	var args []interface{}
	if allRooms {
		query += "(room IS NOT NULL OR type IN ('op.requested','op.decided','op.ran')) AND seq>?"
		args = append(args, afterSeq)
	} else if room == "" {
		query += "room IS NULL AND seq>?"
		args = append(args, afterSeq)
	} else {
		query += "room=? AND seq>?"
		args = append(args, room, afterSeq)
	}
	if beforeSeq > 0 {
		query += " AND seq<?"
		args = append(args, beforeSeq)
	}
	if dm != "" {
		query += " AND home=?"
		args = append(args, dm)
	}
	if len(types) > 0 {
		query += " AND type IN (" + strings.TrimRight(strings.Repeat("?,", len(types)), ",") + ")"
		for _, kind := range types {
			args = append(args, kind)
		}
	}
	if beforeSeq > 0 {
		query += " ORDER BY seq DESC LIMIT ?"
	} else {
		query += " ORDER BY seq LIMIT ?"
	}
	args = append(args, limit)
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []contract.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if beforeSeq > 0 {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	return events, rows.Err()
}

// scanEvent shares the event wire decoding. database/sql requires heterogeneous scan destinations.
func scanEvent(rows *sql.Rows, extra ...interface{}) (contract.Event, error) {
	var event contract.Event
	var at int64
	var room sql.NullString
	var kind contract.EventType
	var fromID string
	var body []byte
	dest := []interface{}{&event.Seq, &event.ID, &at, &room, &kind, &event.From.Kind, &fromID, &body}
	if err := rows.Scan(append(dest, extra...)...); err != nil {
		return event, err
	}
	event.Time = time.UnixMilli(at).UTC()
	event.Room = contract.RoomID(room.String)
	switch event.From.Kind {
	case contract.PrincipalSession:
		event.From.Session = contract.SessionID(fromID)
	case contract.PrincipalOwner:
		event.From.Device = contract.DeviceID(fromID)
	}
	var err error
	event.Body, err = contract.DecodeEventBody(kind, body)
	if err != nil {
		return event, fmt.Errorf("event %d: %w", event.Seq, err)
	}
	return event, nil
}

// EventInRoom finds a room event by ID without exposing events from other rooms.
func (s *Store) EventInRoom(ctx context.Context, room contract.RoomID, id contract.EventID) (contract.Event, error) {
	var seq int64
	if err := s.Read().QueryRowContext(ctx, "SELECT seq FROM events WHERE id=? AND room=?", id, room).Scan(&seq); err != nil {
		return contract.Event{}, err
	}
	return s.Event(ctx, seq)
}

// Event returns one event by sequence, including events from another room.
func (s *Store) Event(ctx context.Context, seq int64) (contract.Event, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT seq,id,at,room,type,from_kind,from_id,body FROM events WHERE seq=?", seq)
	if err != nil {
		return contract.Event{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return contract.Event{}, err
		}
		return contract.Event{}, sql.ErrNoRows
	}
	return scanEvent(rows)
}

// StreamMatches applies the same room and type rules as Events and OwnerEvents.
func StreamMatches(event contract.Event, room contract.RoomID) bool {
	if room != "" {
		return event.Room == room
	}
	switch event.Body.EventType() {
	case "op.requested", "op.decided", "op.ran":
		return true
	case "turn.started", "turn.ended", "session.started", "session.closed", "box.changed", "room.paused", "room.resumed":
		return event.Room != ""
	default:
		return false
	}
}

// Subscribe registers a live feed. Overflow closes the channel; resubscribe
// and replay Events after the last observed sequence.
func (s *Store) Subscribe() (<-chan contract.Event, func()) {
	ch := make(chan contract.Event, 128)
	s.mu.Lock()
	s.subMu.Lock()
	if s.closed {
		close(ch)
	} else {
		if s.subs == nil {
			s.subs = make(map[chan contract.Event]struct{})
		}
		s.subs[ch] = struct{}{}
	}
	s.subMu.Unlock()
	s.mu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subMu.Unlock()
	}
}
func (s *Store) publish(events []contract.Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, event := range events {
		for ch := range s.subs {
			select {
			case ch <- event:
			default:
				delete(s.subs, ch)
				close(ch)
			}
		}
	}
}
