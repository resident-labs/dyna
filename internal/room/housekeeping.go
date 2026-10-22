package room

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// UpdatePreferences changes presentation without changing routing addresses.
// The root lock serializes changes with room creation and deletion.
func (s *Store) UpdatePreferences(ref string, fn func(*Record) error) error {
	rec, err := s.Resolve(ref)
	if err != nil {
		return err
	}
	return s.withRootLock(func() error {
		return s.withRoomLock(rec.ID, func() error {
			current, err := s.readRecordHeld(rec.ID)
			if err != nil {
				return err
			}
			if err = fn(&current); err != nil {
				return err
			}
			return s.writeRecord(current, true)
		})
	})
}

func (s *Store) SetArchived(ref string, archived bool) error {
	return s.UpdatePreferences(ref, func(r *Record) error {
		r.ManualArchive = archived
		if !archived {
			r.LastActivity = s.now()
		}
		return nil
	})
}

func (s *Store) Rename(ref, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("room label must be 1 to 128 bytes on one line")
	}
	return s.UpdatePreferences(ref, func(r *Record) error { r.DisplayName = name; return nil })
}

func (s *Store) SetPinned(ref string, pinned bool, order int) error {
	if order < 0 {
		return fmt.Errorf("pin order must be nonnegative")
	}
	return s.UpdatePreferences(ref, func(r *Record) error { r.Pinned, r.PinOrder = pinned, order; return nil })
}
