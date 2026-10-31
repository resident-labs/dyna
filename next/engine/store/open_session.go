package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// OpenSession reports whether the session remains open.
func (s *Store) OpenSession(ctx context.Context, id contract.SessionID) (Session, bool, error) {
	session, err := s.Session(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	return session, err == nil, err
}
