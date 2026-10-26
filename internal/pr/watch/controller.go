package watch

import (
	"context"
	"errors"
	"log"
	"path/filepath"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

// localDataDir is the data directory this watcher's store lives in, so run
// and session records beside it are read from the same root.
func (s *Service) localDataDir() string {
	if s.Store != nil && s.Store.Root != "" {
		if dir := filepath.Dir(s.Store.Root); dir != "" && dir != "." {
			return dir
		}
	}
	return store.DataDir()
}

// runController reads the controller of the run that owns a run-owned caller:
// the session that started the run, or ControllerRoot when root or the owner
// started it. It reports false when the run record cannot be read, so no
// takeover relies on a guess.
func (s *Service) runController(runID string) (state.Controller, bool) {
	meta, err := runstore.ReadMetaIn(filepath.Join(s.localDataDir(), "runs"), runID)
	if err != nil {
		log.Printf("run %s: reading the run record for its controller: %v", runID, err)
		return "", false
	}
	if meta.ParentSession != "" {
		return state.Controller(meta.ParentSession), true
	}
	return state.ControllerRoot, true
}

// changeController is the controller a change records for the author opening
// it. An author that no run owns records none.
func (s *Service) changeController(author subagent.Info) state.Controller {
	if !author.RunOwned() {
		return ""
	}
	controller, _ := s.runController(author.Run.ID)
	return controller
}

// takeoverRunChange moves the author role of matching change records to a
// run-owned caller of the same controller when the record's current author is
// closed or the room's root session (issue D-252): a later agent of the same
// controller continues the change its earlier run author left behind, through
// the same transfer path as `dyna git pr transfer`. It reports whether a role
// moved. Every record it does not take over keeps the refusal its caller
// would otherwise meet.
func (s *Service) takeoverRunChange(ctx context.Context, author subagent.Info, dir string, matches func(Record) bool) (bool, error) {
	if !author.RunOwned() {
		return false, nil
	}
	controller, ok := s.runController(author.Run.ID)
	if !ok || controller == "" {
		return false, nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return false, err
	}
	moved := false
	for _, old := range st.Records {
		if old.Author == author.ID || old.RoomID != author.RoomID || !matches(*old) {
			continue
		}
		if !s.runTakeoverAllowed(*old, controller) {
			continue
		}
		// expectAuthor keeps an author that changed since the check above
		// from being taken over; the move is then retried by the caller.
		if _, err := s.transfer(ctx, old.Key, "author", author, dir, transferOptions{expectAuthor: old.Author}); err != nil {
			return moved, err
		}
		moved = true
	}
	return moved, nil
}

// runTakeoverAllowed reports whether controller may take the author role of
// old: only an open, watched record of the same controller whose current
// author session is closed or is the room's root session.
func (s *Service) runTakeoverAllowed(old Record, controller state.Controller) bool {
	if old.Controller == "" || old.Controller != controller {
		return false
	}
	if old.State != "open" || !old.Watching {
		return false
	}
	return s.authorRoleVacant(old)
}

// authorRoleVacant reports whether the author session of r can no longer act
// for it: the session is closed or no longer exists, or the room's root
// session holds the role a close handoff left behind.
func (s *Service) authorRoleVacant(r Record) bool {
	sessions := subagent.Store{Root: filepath.Join(s.localDataDir(), "sessions")}
	meta, err := sessions.ReadMeta(r.Author)
	if errors.Is(err, subagent.ErrNotFound) {
		return true
	}
	if err != nil {
		log.Printf("change %s: reading author session %s: %v", r.ChangeID, r.Author, err)
		return false
	}
	if meta.Role == subagent.RoleRoot && meta.RoomID == r.RoomID {
		return true
	}
	return meta.Status == subagent.StatusClosed
}
