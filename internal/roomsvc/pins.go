package roomsvc

import (
	"context"
	"errors"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

// Pin keeps a post at the top of a room's pinned list, recorded as pinned by
// by; an empty by is the room owner. The engine decides by from its caller.
// The caller applies the read-only and health gate, like it does for Post.
func (s *Service) Pin(roomRef, msgID, by string) error {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return err
	}
	if by == "" {
		by = rec.Name
	}
	_, err = s.rooms().Pin(rec.ID, msgID, by)
	return err
}

// Unpin removes a pin. A post that is not pinned is not an error.
func (s *Service) Unpin(ctx context.Context, roomRef, msgID string) error {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return err
	}
	if _, err := authority.ActorIn(ctx, rec.ID); err != nil {
		return err
	}
	return s.rooms().Unpin(rec.ID, msgID)
}

// Pins returns the room's pinned posts as feed posts, newest pin first.
// A pin whose post is gone from the log is left out.
func (s *Service) Pins(roomRef string) ([]Post, error) {
	store := s.rooms()
	rec, err := store.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	if pins, err := store.Pins(rec.ID); err != nil || len(pins) == 0 {
		return nil, err
	}
	return s.pinsOf(rec, s.avatars(rec.ID))
}

// pinsOf reads only pinned messages from the shared room-log index.
func (s *Service) pinsOf(rec room.Record, book avatarBook) ([]Post, error) {
	store := s.rooms()
	pins, err := store.Pins(rec.ID)
	if err != nil || len(pins) == 0 {
		return nil, err
	}
	reads, err := store.OwnerReadIDs(rec.ID)
	if err != nil {
		return nil, err
	}
	var out []Post
	for _, p := range pins {
		m, err := store.Message(rec.ID, p.ID)
		if errors.Is(err, room.ErrPostNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		post := s.expand(decorate(m, rec.Name, reads, book), book)
		post.Pinned, post.PinnedAt, post.PinnedBy = true, p.At, p.By
		out = append(out, post)
	}
	return out, nil
}
