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

// AppendEvent inserts an event in a transaction submitted through Write.
func (s *Store) AppendEvent(tx *sql.Tx, event contract.Event) (int64, error) {
	if tx != s.currentTx {
		return 0, errors.New("event transaction is not the active writer transaction")
	}
	body, err := json.Marshal(event.Body)
	if err != nil {
		return 0, err
	}
	id := string(event.From.Session)
	var room *string
	if event.Room != "" {
		value := string(event.Room)
		room = &value
	}
	// home is the session whose DM holds the event: a DM post, or a turn that answered in a DM.
	var home *contract.SessionID
	switch body := event.Body.(type) {
	case contract.PostCreated:
		if body.Home.Kind == contract.ConversationDM {
			home = &body.Home.Session
		}
	case contract.TurnEnded:
		for _, reply := range body.Replies {
			if reply.Reply.Home.Kind == contract.ConversationDM {
				home = &reply.Reply.Home.Session
				break
			}
		}
	}
	result, err := tx.Exec("INSERT INTO events(id,at,room,type,from_kind,from_id,body,home) VALUES(?,?,?,?,?,?,?,?)", event.ID, event.Time.UnixMilli(), room, event.Body.EventType(), event.From.Kind, id, body, home)
	if err != nil {
		return 0, err
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	event.Seq = seq
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
	return s.eventsPage(ctx, "", afterSeq, 0, limit, []contract.EventType{"op.requested", "op.decided", "op.ran", "turn.started", "turn.ended", "session.closed"}, "", true)
}

func (s *Store) eventsPage(ctx context.Context, room contract.RoomID, afterSeq, beforeSeq int64, limit int, types []contract.EventType, dm contract.SessionID, allRooms bool) ([]contract.Event, error) {
	query := "SELECT seq,id,at,room,type,from_kind,from_id,body FROM events WHERE "
	// database/sql requires a heterogeneous variadic slice for query arguments.
	var args []interface{}
	if allRooms {
		query += "room IS NOT NULL AND seq>?"
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
		var event contract.Event
		var at int64
		var room sql.NullString
		var kind contract.EventType
		var fromID string
		var body []byte
		if err := rows.Scan(&event.Seq, &event.ID, &at, &room, &kind, &event.From.Kind, &fromID, &body); err != nil {
			return nil, err
		}
		event.Time = time.UnixMilli(at).UTC()
		event.Room = contract.RoomID(room.String)
		event.From.Session = contract.SessionID(fromID)
		event.Body, err = contract.DecodeEventBody(kind, body)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", event.Seq, err)
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

// Event returns one event by sequence, including events from another room.
func (s *Store) Event(ctx context.Context, seq int64) (contract.Event, error) {
	var room sql.NullString
	if err := s.Read().QueryRowContext(ctx, "SELECT room FROM events WHERE seq=?", seq).Scan(&room); err != nil {
		return contract.Event{}, err
	}
	events, err := s.Events(ctx, contract.RoomID(room.String), seq-1, 1, nil)
	if err != nil {
		return contract.Event{}, err
	}
	if len(events) == 0 || events[0].Seq != seq {
		return contract.Event{}, sql.ErrNoRows
	}
	return events[0], nil
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
