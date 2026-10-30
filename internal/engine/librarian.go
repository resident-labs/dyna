package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/updaterservice"
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
		Rooms: n.Rooms,
		Defer: s.deferLibrarianWake,
		Notice: func(ctx context.Context, roomID string, ev room.Event) error {
			rec, err := n.Rooms.Resolve(roomID)
			if err != nil {
				return err
			}
			_, err = n.Rooms.PostEventInput(room.PostEventInput{
				RoomID: rec.ID, Event: ev, Producer: room.ProducerLibrarian,
				To: []string{rec.Name}, NeedsRoot: true,
			})
			return err
		},
	}
}

// librarianMainUpdates subscribes enabled repositories to the branch observer.
func (s services) librarianMainUpdates(h hooks.Service) hooks.Service {
	h.ExtraRepositories = func(ctx context.Context) ([]string, error) {
		v, err := settings.Load(settings.DefaultPath())
		if err != nil {
			return nil, err
		}
		cfg, err := (updaterservice.Store{DataDir: store.DataDir()}).Get(ctx)
		if err != nil {
			return nil, err
		}
		if cfg.Enabled && cfg.Source != "" {
			v.LibraryEnabled = append(v.LibraryEnabled, cfg.Source)
		}
		return v.LibraryEnabled, nil
	}
	h.MainUpdated = func(ctx context.Context, event hooks.Event) error {
		changed, err := repoid.Resolve(ctx, event.Repo)
		if err != nil {
			return err
		}
		v, err := settings.Load(settings.DefaultPath())
		if err != nil {
			return err
		}
		var errs []error
		for _, path := range v.LibraryEnabled {
			r, err := repoid.Resolve(ctx, path)
			if err != nil {
				errs = append(errs, fmt.Errorf("resolve enabled library %s: %w", path, err))
				continue
			}
			if r.ID == changed.ID {
				_, err := s.librarianService().Wake(ctx, path)
				errs = append(errs, err)
			}
		}
		cfg, err := (updaterservice.Store{DataDir: store.DataDir()}).Get(ctx)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		if cfg.Enabled && cfg.Source != "" {
			source, err := repoid.Resolve(ctx, cfg.Source)
			if err != nil {
				errs = append(errs, err)
			} else if source.Root == changed.Root {
				_, err := h.StartUpdate(ctx, event, cfg.Source, cfg.ToolPath)
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return h
}

// deferLibrarianWake keeps one deadline per repository. A later deferral for the
// same repository replaces the earlier one.
func (s services) deferLibrarianWake(repo string, at time.Time, wake func()) {
	if s.timers == nil {
		return
	}
	s.timers.at(librarianWakeKey(repo), at, wake)
}

// librarianLastRun returns the last feature-map pass, if one has started.
func (s services) librarianLastRun(ctx context.Context, repo string) (*time.Time, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	st, err := (librarianstate.Store{DataDir: store.DataDir()}).Get(r.ID)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if st.LastRun.IsZero() {
		return nil, nil
	}
	return &st.LastRun, nil
}

// librarianDeferred reports when the deferred wake of a repository is due. It is
// nil when no pass is deferred.
func (s services) librarianDeferred(ctx context.Context, repo string) (*time.Time, error) {
	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	if s.timers == nil {
		return nil, nil
	}
	at, ok := s.timers.due(librarianWakeKey(r.Main))
	if !ok {
		return nil, nil
	}
	return &at, nil
}

// restoreLibrarianWakes runs one wake for each enabled repository when the engine
// starts. The deferred wakes lived in memory, so each wake defers its own next pass.
func (s services) restoreLibrarianWakes(ctx context.Context) {
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		log.Printf("restore librarian wakes: %v", err)
		return
	}
	svc := s.librarianService()
	for _, repo := range v.LibraryEnabled {
		if err := svc.Restore(ctx, repo); err != nil {
			log.Printf("restore librarian wake of %s: %v", repo, err)
		}
	}
}

func librarianWakeKey(repo string) string {
	return timerLibrarianWake + ":" + repo
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

// wakeLibrarianSmellsNow starts the smell pass at once for a repository, or for
// the repository of the named room. The librarian service checks the caller.
func (s services) wakeLibrarianSmellsNow(ctx context.Context, repo, roomRef string) (librarian.Result, error) {
	as, _, err := librarianActor(ctx)
	if err != nil {
		return librarian.Result{}, err
	}
	if roomRef != "" {
		rec, err := s.rooms.Notify.Rooms.Resolve(roomRef)
		if err != nil {
			return librarian.Result{}, err
		}
		if rec.Repo == "" {
			return librarian.Result{}, fmt.Errorf("room %q has no repository", rec.Name)
		}
		repo = rec.Repo
	}
	callerRoom := ""
	if as.Room != "" {
		rec, err := s.rooms.Notify.Rooms.Resolve(as.Room)
		if err != nil {
			return librarian.Result{}, err
		}
		callerRoom = rec.ID
	}
	return s.librarianService().WakeSmellsNow(ctx, as, callerRoom, repo)
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
	if err := states.CheckSmell("library smell", r.ID, as); err != nil {
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
