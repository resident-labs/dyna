package engine

import (
	"context"
	"fmt"
	"log"
	"os"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func (s services) addHook(ctx context.Context, repo string, hook hooks.Hook) error {
	by, err := actor(ctx)
	if err != nil {
		return err
	}
	hook.AddedBy = by
	hook.EditedBy, hook.EditedAt = "", nil
	return s.hooks.Add(ctx, repo, hook)
}

// addGlobalHook stores a hook that belongs to no repository.
func (s services) addGlobalHook(ctx context.Context, hook hooks.Hook) error {
	by, err := actor(ctx)
	if err != nil {
		return err
	}
	hook.AddedBy = by
	hook.EditedBy, hook.EditedAt = "", nil
	return s.hooks.AddGlobal(ctx, hook)
}

func (s services) librarianService() librarian.Service {
	n := s.rooms.Notify
	return librarian.Service{
		DataDir:  store.DataDir(),
		Sessions: librarian.Live{Manager: n.Sessions, Rooms: n.Rooms}, Issues: n.Rooms,
		ListHooks: s.hooks.List, AddHook: s.addHook, RemoveHook: s.hooks.Remove,
	}
}

func librarianActor(ctx context.Context) (authority.Actor, string, error) {
	caller, local := websvc.CallerFromContext(ctx)
	return mutationActor(caller, local, authority.Of(ctx), callspec.ClaimOf(ctx), enforceCallers, log.Printf, "changing the librarian")
}

func (s services) enableLibrarian(ctx context.Context, repo, ref, cwd string) (librarian.Enabled, error) {
	as, by, err := librarianActor(ctx)
	if err != nil {
		return librarian.Enabled{}, err
	}
	// Resolve or create a room only after the shared enable guard permits it.
	if err := authority.Check("library enable", as, nil); err != nil {
		return librarian.Enabled{}, err
	}
	var rec room.Record
	if as.Room != "" {
		rec, err = s.rooms.Notify.Rooms.Resolve(as.Room)
		if err == nil && ref != "" {
			var want room.Record
			want, err = s.rooms.Notify.Rooms.Resolve(ref)
			if err == nil && want.ID != rec.ID {
				err = fmt.Errorf("Dyna work inherits room %q and cannot switch to %q", rec.Name, want.Name)
			}
		}
	} else if ref != "" {
		rec, err = s.rooms.Notify.Rooms.Resolve(ref)
	} else {
		rec, err = s.rooms.Notify.Rooms.EnsureRepoRoom(cwd, by)
	}
	if err != nil {
		return librarian.Enabled{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return librarian.Enabled{}, err
	}
	r, err := s.librarianService().Enable(ctx, as, repo, rec.ID, exe)
	return librarian.Enabled{Repo: r, Room: rec.Name}, err
}

func (s services) disableLibrarian(ctx context.Context, repo string) (repoid.Repo, error) {
	as, _, err := librarianActor(ctx)
	if err != nil {
		return repoid.Repo{}, err
	}
	return s.librarianService().Disable(ctx, as, repo)
}

func (s services) wakeLibrarian(ctx context.Context, repo string) (librarian.Result, error) {
	if _, _, err := librarianActor(ctx); err != nil {
		return librarian.Result{}, err
	}
	return s.librarianService().Wake(ctx, repo)
}

func (s services) fileLibrarySmell(ctx context.Context, repo string, sm librarian.Smell) (room.Issue, error) {
	as, by, err := librarianActor(ctx)
	if err != nil {
		return room.Issue{}, err
	}
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return room.Issue{}, err
	}
	states := librarianstate.Store{DataDir: store.DataDir()}
	if err := states.CheckEdit("library smell", r.ID, as); err != nil {
		return room.Issue{}, err
	}
	st, err := states.Get(r.ID)
	if err != nil {
		return room.Issue{}, err
	}
	rec, err := s.rooms.Notify.Rooms.Resolve(st.RoomID)
	if err != nil {
		return room.Issue{}, err
	}
	switch {
	case by == "owner":
		by = rec.Name
	case by == unverified:
		by = rec.Name + ":" + unverified
	case as.Session != "":
		info, err := s.rooms.Notify.Sessions.Store.Info(as.Session)
		if err != nil {
			return room.Issue{}, err
		}
		by = info.Address()
	}
	return s.librarianService().FileSmell(ctx, as, r.Root, by, sm)
}
