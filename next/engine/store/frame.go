package store

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// FrameSession reads a session name and the relationship to its viewer.
func (s *Store) FrameSession(ctx context.Context, viewer, session contract.SessionID) (string, contract.SessionID, contract.SessionID, error) {
	var name string
	var parent, viewerParent contract.SessionID
	err := s.Read().QueryRowContext(ctx, "SELECT name,COALESCE(parent,''),(SELECT COALESCE(parent,'') FROM sessions WHERE id=?) FROM sessions WHERE id=?", viewer, session).Scan(&name, &parent, &viewerParent)
	return name, parent, viewerParent, err
}
