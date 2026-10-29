package room

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"dyna-agent/internal/repoid"
)

// UpdatePreferences changes room settings without changing routing addresses.
// The root lock serializes changes with room creation and deletion.
func (s *Store) UpdatePreferences(ref string, fn func(*Record) error) error {
	rec, err := s.Resolve(ref)
	if err != nil {
		return err
	}
	return s.withRootLock(func() error {
		return s.withRoomLock(rec.ID, func() error {
			current, err := s.readRecord(rec.ID)
			if err != nil {
				return err
			}
			if err = fn(&current); err != nil {
				return err
			}
			return s.writeRecord(current)
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

// SetRootProfile chooses the next internal root's profile. Empty inherits the
// global root.profile setting; existing sessions keep their current profile.
func (s *Store) SetRootProfile(ref, name string) error {
	return s.UpdatePreferences(ref, func(r *Record) error {
		r.RootProfile = strings.TrimSpace(name)
		return nil
	})
}

// SetUpstream stores the parent repository of this room's fork checkout.
// Empty clears it. The caller passes a value from ParseUpstream.
func (s *Store) SetUpstream(ref, upstream string) error {
	return s.UpdatePreferences(ref, func(r *Record) error {
		r.Upstream = upstream
		return nil
	})
}

// ParseUpstream reads a room's upstream repository, as the CLI and the app
// accept it. "off" or empty clears it. A repository such as github.com/owner/repo
// or https://github.com/owner/repo.git is stored as host/owner/repo.
func ParseUpstream(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.EqualFold(trimmed, "off") {
		return "", nil
	}
	remote := trimmed
	if !strings.ContainsAny(remote, ":") {
		remote = "https://" + remote
	}
	id, err := repoid.RemoteIdentity("", remote)
	if err != nil {
		return "", err
	}
	if parts := strings.Split(id, "/"); len(parts) != 3 || slices.Contains(parts, "") {
		return "", fmt.Errorf("upstream %q must name one repository as host/owner/repo", value)
	}
	return id, nil
}

// SetLibraryPin stores this room's librarian profile. set false clears the
// pin so the room uses the global library.profile again. An empty profile
// with set true is auto for this room only.
func (s *Store) SetLibraryPin(ref string, set bool, profile string) error {
	return s.UpdatePreferences(ref, func(r *Record) error {
		r.LibraryPinSet = set
		r.LibraryPin = ""
		if set {
			r.LibraryPin = strings.TrimSpace(profile)
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
