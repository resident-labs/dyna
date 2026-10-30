package librarian

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/repoid"
)

// Enable lists the repository in library.enabled, records the room that
// holds its librarian. It is idempotent; a second call refreshes the room.
func (s Service) Enable(ctx context.Context, actor authority.Actor, repoDir, roomID, exe string) (repoid.Repo, error) {
	if err := authority.Check("library enable", actor, nil); err != nil {
		return repoid.Repo{}, err
	}
	r, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return r, err
	}
	// The session's name is fixed per room, so a room holds one librarian.
	held, err := librarianstate.Store{DataDir: s.DataDir}.RoomRepo(roomID)
	if err != nil {
		return r, err
	}
	if held != "" && held != r.ID {
		return r, fmt.Errorf("this room already holds the librarian of another repository; use a room for %s", r.Main)
	}
	v, err := s.settings()
	if err != nil {
		return r, err
	}
	if !v.LibraryOn(r.Main) {
		v.LibraryEnabled = append(v.LibraryEnabled, filepath.Clean(r.Main))
		if err := v.Save(); err != nil {
			return r, err
		}
	}
	// Moving to another room closes the old room's session first, so no
	// later librarian in that room can adopt it.
	old, err := librarianstate.Store{DataDir: s.DataDir}.Get(r.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	if err == nil && old.RoomID != roomID {
		for _, id := range []string{old.Session, old.SmellSession} {
			if id != "" {
				if err := s.Sessions.Close(ctx, id); err != nil {
					return r, err
				}
			}
		}
	}
	if _, err := (librarianstate.Store{DataDir: s.DataDir}).Update(r.ID, true, func(st *librarianstate.State) error {
		if st.RoomID != roomID {
			st.Session, st.Pending, st.PendingTurn = "", "", 0
			st.SmellSession, st.SmellPending, st.SmellPendingTurn = "", "", 0
		}
		st.Main, st.RoomID = r.Main, roomID
		return nil
	}); err != nil {
		return r, err
	}
	return r, nil
}

// Disable removes the repository from library.enabled and closes the librarian
// session. The map stays.
func (s Service) Disable(ctx context.Context, actor authority.Actor, repoDir string) (repoid.Repo, error) {
	if err := authority.Check("library disable", actor, nil); err != nil {
		return repoid.Repo{}, err
	}
	r, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return r, err
	}
	v, err := s.settings()
	if err != nil {
		return r, err
	}
	if v.LibraryOn(r.Main) {
		v.LibraryEnabled = slices.DeleteFunc(v.LibraryEnabled, func(p string) bool { return p == filepath.Clean(r.Main) })
		if err := v.Save(); err != nil {
			return r, err
		}
	}
	st, err := librarianstate.Store{DataDir: s.DataDir}.Get(r.ID)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	for _, id := range []string{st.Session, st.SmellSession} {
		if id != "" {
			if err := s.Sessions.Close(ctx, id); err != nil {
				return r, err
			}
		}
	}
	_, err = librarianstate.Store{DataDir: s.DataDir}.Update(r.ID, false, func(st *librarianstate.State) error { st.Session, st.SmellSession = "", ""; return nil })
	return r, err
}

// Enabled is the repository and room used by a successful enable call.
type Enabled struct {
	Repo repoid.Repo
	Room string
}
