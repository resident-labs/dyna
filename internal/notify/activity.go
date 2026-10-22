package notify

import (
	"path/filepath"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shells"
)

// ActiveRoomIDs aggregates activity from the stores that own its liveness rules.
func (s *Service) ActiveRoomIDs() map[string]bool {
	store := s.sessionStore()
	root := store.Root
	if root == "" {
		root = runstore.SessionsDir()
	}
	data := filepath.Dir(root)
	live := store.ActiveRoomIDs()
	for id := range runstore.ActiveRoomIDs() {
		live[id] = true
	}
	for id := range (&shells.Store{Root: filepath.Join(data, "shells")}).ActiveRoomIDs() {
		live[id] = true
	}
	return live
}
