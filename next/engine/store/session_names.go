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

// SessionContextNames returns a session's room and parent names.
func (s *Store) SessionContextNames(ctx context.Context, session contract.SessionID) (string, string, error) {
	var room, parent string
	err := s.Read().QueryRowContext(ctx, "SELECT r.name,COALESCE(p.name,'') FROM sessions s JOIN rooms r ON r.id=s.room LEFT JOIN sessions p ON p.id=s.parent WHERE s.id=?", session).Scan(&room, &parent)
	return room, parent, err
}
