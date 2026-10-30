package roomsvc

import (
	"context"
	"errors"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

// SetSenderMute lets the owner or the recipient room's root mute a sender.
// A zero duration removes the mute.
func (s *Service) SetSenderMute(ctx context.Context, roomRef, kind, ref string, durationSeconds int64) error {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return err
	}
	actor, err := s.issueCaller(ctx, rec.ID)
	if err != nil {
		return err
	}
	if actor.Role != authority.Owner && actor.Role != authority.Root {
		return errors.New("only the owner or the room's root can mute a sender")
	}
	if durationSeconds < 0 || durationSeconds > int64((30*24*time.Hour)/time.Second) {
		return errors.New("mute duration must be between zero and 30 days")
	}
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "@")
	var id string
	muteKind := room.SenderMuteKind(kind)
	switch muteKind {
	case room.SenderMuteSession:
		info, err := s.sessions().Resolve(ref)
		if err != nil {
			return err
		}
		id = info.ID
	case room.SenderMuteRoom:
		sender, err := s.rooms().Resolve(ref)
		if err != nil {
			return err
		}
		id = sender.ID
	default:
		return errors.New("sender kind must be session or room")
	}
	var until time.Time
	if durationSeconds != 0 {
		until = time.Now().Add(time.Duration(durationSeconds) * time.Second)
	}
	return s.rooms().SetSenderMute(rec.ID, muteKind, id, until)
}
