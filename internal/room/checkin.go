package room

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CheckIn is a room's parent check-in cadence, stored on the room record in
// SQLite with the room's other settings. The zero value inherits the parent
// check-ins hook's global interval.
type CheckIn struct {
	// Off stops parent check-ins in this room.
	Off bool `json:"off,omitempty"`
	// Every overrides the hook's global interval when positive.
	Every time.Duration `json:"every,omitempty"`
}

// Validate rejects a setting no pass can apply.
func (c CheckIn) Validate() error {
	if c.Off && c.Every != 0 {
		return fmt.Errorf("turn parent check-ins off or set their interval, not both")
	}
	if c.Every < 0 {
		return fmt.Errorf("parent check-in interval cannot be negative")
	}
	if c.Every != 0 && c.Every < time.Minute {
		return fmt.Errorf("parent check-in interval must be at least 1m")
	}
	return nil
}

// Effective is the interval this room's parents check in with, given the
// hook's global interval. Zero means this room gets no check-ins.
func (c CheckIn) Effective(global time.Duration) time.Duration {
	switch {
	case c.Off:
		return 0
	case c.Every > 0:
		return c.Every
	default:
		return global
	}
}

// String is the value the CLI and the app show and accept: default, off, or a
// duration such as 30m.
func (c CheckIn) String() string {
	switch {
	case c.Off:
		return "off"
	case c.Every > 0:
		return c.Every.String()
	default:
		return "default"
	}
}

// ParseCheckIn reads one check-in value, as the CLI and the app accept it:
// "default" or empty keeps the hook's global interval, "off" stops this room's
// check-ins, and a duration overrides the interval.
func ParseCheckIn(value string) (CheckIn, error) {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "default":
		return CheckIn{}, nil
	case "off":
		return CheckIn{Off: true}, nil
	}
	every, err := time.ParseDuration(trimmed)
	if err != nil {
		return CheckIn{}, fmt.Errorf("parent check-in interval must be a duration, off or default, not %q", trimmed)
	}
	out := CheckIn{Every: every}
	return out, out.Validate()
}

// SetCheckIn stores the room's parent check-in setting. ref is a name or ID.
func (s *Store) SetCheckIn(ctx context.Context, ref string, setting CheckIn) (Record, error) {
	if err := setting.Validate(); err != nil {
		return Record{}, err
	}
	resolved, err := s.Resolve(ref)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = s.WithRoomSnapshot(ctx, resolved.ID, func(rec Record) error {
		rec.CheckIn = setting
		out = rec
		return s.writeRecord(rec)
	})
	return out, err
}
