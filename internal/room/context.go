package room

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/runstore"
)

// ResolveContext resolves an explicit room, the room inherited by this
// process, or the room of the git repo that holds the working directory.
// requireStamp rejects unstamped Dyna processes.
func (s *Store) ResolveContext(ref string, requireStamp bool) (Record, error) {
	ref = strings.TrimSpace(ref)
	stamp, stamped := mode.StampFromEnv()
	inherited := func() (Record, error) {
		if stamp.RoomID != "" {
			return s.Resolve(stamp.RoomID)
		}
		return s.Resolve(stamp.Room)
	}
	if mode.InsideDyna() && stamped {
		rec, err := inherited()
		if err != nil {
			return Record{}, err
		}
		if ref != "" {
			want, err := s.Resolve(ref)
			if err != nil {
				return Record{}, err
			}
			if want.ID != rec.ID {
				return Record{}, fmt.Errorf("Dyna work inherits room %q and cannot switch to %q", rec.Name, want.Name)
			}
		}
		return rec, nil
	}
	if mode.InsideDyna() && requireStamp {
		return Record{}, errors.New("this Dyna process has no room stamp; it cannot start a session")
	}
	if ref != "" {
		return s.Resolve(ref)
	}
	if stamped {
		return inherited()
	}
	return s.CurrentRepoRoom()
}

// ErrOutsideRepo means the working directory is in no git repo, so it has no
// room. Other errors, such as a room name taken by another repo, are not it.
var ErrOutsideRepo = errors.New("not in a repo; pass --room <name>")

// CurrentRepoRoom returns the room of the git repo that holds the working
// directory, creating it on first use.
func (s *Store) CurrentRepoRoom() (Record, error) {
	dir, err := os.Getwd()
	if err != nil {
		return Record{}, err
	}
	rec, err := s.EnsureRepoRoom(dir, runstore.OwnerFromEnv())
	if errors.Is(err, ErrNotRepo) {
		return Record{}, ErrOutsideRepo
	}
	return rec, err
}
