package store

import (
	"context"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
)

// DiscordTurnInputs returns Discord inputs actually taken into the current turn,
// including inputs delivered after dispatch, not merely messages queued for it.
func (s *Store) DiscordTurnInputs(ctx context.Context, session contract.SessionID) ([]contract.DiscordMessage, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT e.body FROM deliveries d JOIN turns t ON t.id=d.turn JOIN events e ON e.seq=d.seq WHERE t.session=? AND t.ended_at IS NULL AND d.to_id=? AND e.type='discord.message' ORDER BY e.seq`, session, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []contract.DiscordMessage
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var m contract.DiscordMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
