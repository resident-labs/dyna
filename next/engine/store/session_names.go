package store

import (
	"context"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// SessionNames returns the names of the requested sessions, including closed sessions.
func (s *Store) SessionNames(ctx context.Context, ids []contract.SessionID) (map[contract.SessionID]string, error) {
	names := map[contract.SessionID]string{}
	if len(ids) == 0 {
		return names, nil
	}
	// database/sql requires a heterogeneous argument slice.
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.Read().QueryContext(ctx, "SELECT id,name FROM sessions WHERE id IN (?"+strings.Repeat(",?", len(ids)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id contract.SessionID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}
