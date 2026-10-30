package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// RecordPressure records one box.pressure event in the box's room.
// The event wakes nobody, so no delivery row is written.
func (s *Service) RecordPressure(ctx context.Context, box contract.BoxID, kind contract.PressureKind) error {
	record, err := s.store.Box(ctx, box)
	if err != nil {
		return err
	}
	session, err := s.store.Session(ctx, record.Session)
	if err != nil {
		return err
	}
	return s.Record(ctx, session.Room, contract.BoxPressure{Box: box, Kind: kind})
}
