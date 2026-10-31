package engine

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// FailStaleStarts fails boxes that have waited more than 60 seconds for hello.
func (s *Service) FailStaleStarts(ctx context.Context) error {
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return err
	}
	cutoff := s.now().Add(-60 * time.Second)
	for _, b := range boxes {
		if b.State != contract.BoxStarting || !b.Updated.Before(cutoff) {
			continue
		}
		unlock := s.lockBox(b.Session)
		current, err := s.store.Box(ctx, b.ID)
		if err == nil && current.State == contract.BoxStarting && current.Updated.Before(cutoff) {
			if err = s.boxes.Remove(ctx, b.ID); err != nil {
				unlock()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			session, e := s.store.Session(ctx, b.Session)
			err = e
			if err == nil {
				_, err = s.transition(ctx, session.Room, current, contract.BoxFailed, "no hello within 60 s", contract.CauseNoHello)
				if errors.Is(err, sql.ErrNoRows) {
					err = nil // Close won the state change and will finish removal.
				}
			}
		}
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
