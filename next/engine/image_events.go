package engine

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
)

// Record appends an image build event to its room.
func (s *Service) Record(ctx context.Context, room contract.RoomID, body contract.EventBody) error {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, body)
	if err != nil {
		return err
	}
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := s.store.AppendEvent(tx, event)
		return err
	})
}
