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
	var id string
	switch event.From.Kind {
	case contract.PrincipalSession:
		id = string(event.From.Session)
	case contract.PrincipalMember:
		id = string(event.From.Member)
	}
	var room *string
	if event.Room != "" {
		value := string(event.Room)
		room = &value
	}
	result, err := tx.Exec("INSERT INTO events(id,at,room,type,from_kind,from_id,body) VALUES(?,?,?,?,?,?,?)", event.ID, event.Time.UnixMilli(), room, event.Body.EventType(), event.From.Kind, id, body)
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
	query := "SELECT seq,id,at,room,type,from_kind,from_id,body FROM events WHERE "
	// database/sql requires a heterogeneous variadic slice for query arguments.
	var args []interface{}
	if room == "" {
		query += "room IS NULL AND seq>?"
		args = append(args, afterSeq)
	} else {
		query += "room=? AND seq>?"
		args = append(args, room, afterSeq)
	}
	if len(types) > 0 {
		query += " AND type IN (" + strings.TrimRight(strings.Repeat("?,", len(types)), ",") + ")"
		for _, kind := range types {
			args = append(args, kind)
		}
	}
	query += " ORDER BY seq LIMIT ?"
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
		switch event.From.Kind {
		case contract.PrincipalSession:
			event.From.Session = contract.SessionID(fromID)
		case contract.PrincipalMember:
			event.From.Member = contract.MemberID(fromID)
		}
		event.Body, err = contract.DecodeEventBody(kind, body)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", event.Seq, err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
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
