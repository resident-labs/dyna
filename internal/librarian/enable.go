package librarian

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/repoid"
)

// Enable lists the repository in library.enabled, records the room that
// holds its librarian, and installs the librarian hooks that run exe.
// It is idempotent; a second call refreshes the hooks and the room.
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
	if err == nil && old.RoomID != roomID && old.Session != "" {
		if err := s.Sessions.Close(ctx, old.Session); err != nil {
			return r, err
		}
	}
	if _, err := (librarianstate.Store{DataDir: s.DataDir}).Update(r.ID, true, func(st *librarianstate.State) error {
		if st.RoomID != roomID {
			st.Session, st.Pending, st.PendingTurn = "", "", 0
		}
		st.Main, st.RoomID = r.Main, roomID
		return nil
	}); err != nil {
		return r, err
	}
	have, err := s.installed(ctx, r.Main)
	if err != nil {
		return r, err
	}
	for _, hook := range hooks.LibrarianHooks(exe, r.Main) {
		if old, ok := have[hook.Name]; ok {
			if old.Command == hook.Command && old.Event == hook.Event && old.EveryMinutes == hook.EveryMinutes {
				continue
			}
			if err := s.RemoveHook(ctx, r.Main, hook.Name); err != nil {
				return r, err
			}
		}
		if err := s.AddHook(ctx, r.Main, hook); err != nil {
			return r, err
		}
	}
	return r, nil
}

// Disable removes the repository from library.enabled, removes the hooks
// and closes the librarian session. The map stays.
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
	have, err := s.installed(ctx, r.Main)
	if err != nil {
		return r, err
	}
	for _, hook := range hooks.LibrarianHooks("", r.Main) {
		if _, ok := have[hook.Name]; ok {
			if err := s.RemoveHook(ctx, r.Main, hook.Name); err != nil {
				return r, err
			}
		}
	}
	st, err := librarianstate.Store{DataDir: s.DataDir}.Get(r.ID)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if st.Session == "" {
		return r, nil
	}
	if err := s.Sessions.Close(ctx, st.Session); err != nil {
		return r, err
	}
	_, err = librarianstate.Store{DataDir: s.DataDir}.Update(r.ID, false, func(st *librarianstate.State) error { st.Session = ""; return nil })
	return r, err
}

// installed returns the repository's hooks by name, so enable and disable
// change only the librarian hooks that differ.
func (s Service) installed(ctx context.Context, main string) (map[string]hooks.Hook, error) {
	list, err := s.ListHooks(ctx, main)
	if err != nil {
		return nil, err
	}
	have := make(map[string]hooks.Hook, len(list))
	for _, hk := range list {
		have[hk.Name] = hk
	}
	return have, nil
}

// Enabled is the repository and room used by a successful enable call.
type Enabled struct {
	Repo repoid.Repo
	Room string
}
