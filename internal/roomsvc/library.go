package roomsvc

import (
	"context"
	"errors"

	"dyna-agent/internal/librarian"
	"dyna-agent/internal/store"
)

// Library is the read-only feature map of a room's repository.
func (s *Service) Library(roomRef string) (librarian.LibraryView, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return librarian.LibraryView{}, err
	}
	if r.Repo == "" {
		return librarian.LibraryView{}, errors.New("this room has no repository")
	}
	return librarian.View(context.Background(), store.DataDir(), r.Repo)
}
