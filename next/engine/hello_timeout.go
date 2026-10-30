package engine

import (
	"context"
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
			session, e := s.store.Session(ctx, b.Session)
			err = e
			if err == nil {
				_, err = s.transition(ctx, session.Room, current, contract.BoxFailed, "no hello within 60 s")
			}
		}
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
