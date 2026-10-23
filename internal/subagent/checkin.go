package subagent

import (
	"context"
	"errors"
)

// DeliverCheckIn stores a check-in only between turns. The locks make this
// atomic with starting a turn, so check-ins cannot become live steering.
func (m *Manager) DeliverCheckIn(ctx context.Context, id string, write func() (string, error)) (bool, error) {
	deferred := false
	err := m.store().withLock(id, func() error {
		info, err := m.store().Inspect(id)
		if err != nil {
			return err
		}
		if info.Status == StatusClosed {
			return nil
		}
		if info.Status == StatusRunning || info.Status == StatusQueued || m.RoomPaused(info.RoomID) {
			deferred = true
			return nil
		}
		lock, err := m.store().tryTurnLock(id)
		if errors.Is(err, ErrBusy) {
			deferred = true
			return nil
		}
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err = write()
		return err
	})
	return err == nil && !deferred, err
}
