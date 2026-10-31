package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
)

// SetRoomLimits updates a room and appends its event atomically.
func (s *Store) SetRoomLimits(ctx context.Context, room contract.RoomID, cpus int, memBytes int64, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE rooms SET cpus=?,mem_bytes=? WHERE id=?", cpus, memBytes, room)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}
