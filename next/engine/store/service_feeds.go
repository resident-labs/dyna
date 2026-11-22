package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// RecordServiceFeedMessage dedupes an inbound message by (service, id), updates the feed's counters, and appends a
// service.message event stamped with the socket's room and service name. It reports whether the id was already seen.
func (s *Store) RecordServiceFeedMessage(
	ctx context.Context,
	serviceID contract.ServiceID,
	room contract.RoomID,
	name contract.ServiceName,
	msgID, author, text string,
	data json.RawMessage,
	now time.Time,
	newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error),
) (bool, error) {
	duplicate := false
	err := s.Write(ctx, func(tx *sql.Tx) error {
		duplicate = false
		var want contract.ServiceWant
		if err := tx.QueryRowContext(ctx, "SELECT want FROM services WHERE id=? AND room=?", serviceID, room).Scan(&want); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if want == contract.ServiceWantRemove {
			return ErrNotFound
		}
		res, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO service_feed_messages(service,id,at) VALUES(?,?,?)", serviceID, msgID, now.UnixMilli())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			duplicate = true
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE services SET feed_in=feed_in+1,feed_last_at=? WHERE id=?", now.UnixMilli(), serviceID); err != nil {
			return err
		}
		event, err := newEvent(room, contract.ServiceMessage{
			Service: name,
			ID:      msgID,
			Author:  author,
			Text:    text,
			Data:    data,
		})
		if err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return duplicate, err
}

// EnqueueServiceOutbox queues one outbound message for a feed service, increments feed_out, and appends the service.sent audit event.
func (s *Store) EnqueueServiceOutbox(
	ctx context.Context,
	serviceID contract.ServiceID,
	room contract.RoomID,
	name contract.ServiceName,
	msgID, to, text string,
	now time.Time,
	newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error),
) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var want contract.ServiceWant
		if err := tx.QueryRowContext(ctx, "SELECT want FROM services WHERE id=? AND room=?", serviceID, room).Scan(&want); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if want == contract.ServiceWantRemove {
			return fmt.Errorf("service %s is being removed: %w", name, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO service_outbox(id,service,room,recipient,text,created_at,acked_at) VALUES(?,?,?,?,?,?,0)", msgID, serviceID, room, to, text, now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE services SET feed_out=feed_out+1 WHERE id=?", serviceID); err != nil {
			return err
		}
		event, err := newEvent(room, contract.ServiceSent{
			Service: name,
			ID:      msgID,
			To:      to,
			Text:    text,
		})
		if err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}

// PendingServiceOutbox returns up to limit unacknowledged outbound messages for serviceID, oldest first.
func (s *Store) PendingServiceOutbox(ctx context.Context, serviceID contract.ServiceID, limit int) ([]contract.FeedOutboxMessage, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,recipient,text,created_at FROM service_outbox WHERE service=? AND acked_at=0 ORDER BY created_at,rowid LIMIT ?", serviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.FeedOutboxMessage{}
	for rows.Next() {
		var m contract.FeedOutboxMessage
		var createdAt int64
		if err := rows.Scan(&m.ID, &m.To, &m.Text, &createdAt); err != nil {
			return nil, err
		}
		m.At = time.UnixMilli(createdAt).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// AckServiceOutbox acknowledges one outbound message belonging to serviceID. Repeated acks are idempotent.
func (s *Store) AckServiceOutbox(ctx context.Context, serviceID contract.ServiceID, msgID string, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var acked int64
		err := tx.QueryRowContext(ctx, "SELECT acked_at FROM service_outbox WHERE id=? AND service=?", msgID, serviceID).Scan(&acked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if acked != 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, "UPDATE service_outbox SET acked_at=? WHERE id=? AND service=?", now.UnixMilli(), msgID, serviceID)
		return err
	})
}
