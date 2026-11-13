package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

// TurnTookPost includes inputs steered into a running turn, which are not in
// that turn's original turn.started event.
func (s *Store) TurnTookPost(ctx context.Context, session contract.SessionID, turn contract.TurnID, post contract.EventID) (bool, error) {
	var took bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.id=? AND d.to_kind=? AND d.to_id=? AND d.turn=?)`, post, contract.PrincipalSession, session, turn).Scan(&took)
	return took, err
}
