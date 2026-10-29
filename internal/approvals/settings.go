package approvals

import (
	"errors"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

// Settings reads or changes the owner-only approval preferences of one room.
func (s *Service) Settings(actor authority.Actor, in SettingsInput) (Settings, error) {
	if actor.Role != authority.Owner {
		return Settings{}, errors.New("only the owner can change approval settings")
	}
	rec, err := s.roomFor(actor, in.Room)
	if err != nil {
		return Settings{}, err
	}
	if in.Set {
		if in.Wait <= 0 || in.Expiry <= 0 {
			return Settings{}, errors.New("approval wait and expiry must be positive")
		}
		if err := s.Rooms.UpdatePreferences(rec.ID, func(r *room.Record) error {
			r.ApprovalWait, r.ApprovalExpiry = in.Wait, in.Expiry
			return nil
		}); err != nil {
			return Settings{}, err
		}
		rec.ApprovalWait, rec.ApprovalExpiry = in.Wait, in.Expiry
	}
	return effectiveSettings(rec), nil
}

func effectiveSettings(rec room.Record) Settings {
	settings := Settings{Wait: rec.ApprovalWait, Expiry: rec.ApprovalExpiry}
	if settings.Wait == 0 {
		settings.Wait = 60 * time.Second
	}
	if settings.Expiry == 0 {
		settings.Expiry = 24 * time.Hour
	}
	return settings
}
