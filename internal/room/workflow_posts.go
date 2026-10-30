package room

import (
	"context"
	"database/sql"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// WorkflowPostReservation gives one workflow post a stable room and post ID.
type WorkflowPostReservation struct {
	RoomID    string
	PostID    string
	Delivered bool
}

// ReserveWorkflowPost creates a reservation or returns the existing one.
func (s *Store) ReserveWorkflowPost(ctx context.Context, key, roomID string) (WorkflowPostReservation, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return WorkflowPostReservation{}, err
	}
	var reservation WorkflowPostReservation
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_post_reservations(step_key,room_id,post_id) VALUES(?,?,?) ON CONFLICT(step_key) DO NOTHING`, key, roomID, newMessageID()); err != nil {
			return err
		}
		return conn.QueryRowContext(ctx, `SELECT room_id,post_id,delivered FROM workflow_post_reservations WHERE step_key=?`, key).Scan(&reservation.RoomID, &reservation.PostID, &reservation.Delivered)
	})
	return reservation, err
}

// CompleteWorkflowPost marks a reserved post after it has been stored.
func (s *Store) CompleteWorkflowPost(ctx context.Context, key string) error {
	db, err := s.postIndexDB()
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE workflow_post_reservations SET delivered=1 WHERE step_key=?`, key)
		return err
	})
}
