package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// Recover cleans up allocations interrupted before container startup completed.
func (s *Service) Recover(ctx context.Context) error {
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		if b.State != contract.BoxCreating {
			continue
		}
		session, err := s.store.Session(ctx, b.Session)
		if err != nil {
			return err
		}
		if err := s.boxes.Remove(ctx, b.ID); err != nil {
			return err
		}
		if _, err := s.transition(ctx, session.Room, b, contract.BoxFailed, "engine restarted during create"); err != nil {
			return err
		}
	}
	return nil
}
