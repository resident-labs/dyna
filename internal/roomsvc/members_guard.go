package roomsvc

import (
	"path/filepath"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// membersChanged reports whether a tick's members scan has anything new to
// read (requirement 16). The engine's own session writes wake Watch with
// their session IDs (SessionChanged). A scan advances the guard only when
// it covers every write in the captured revision. A partial targeted wake
// leaves the next tick able to find another process's unwoken write. Each
// fact comes from a row whose write moves MembersRevision, or from a room
// record's name and seat, compared here.
//
// The guard also catches current session writes made outside the engine,
// which do not wake Watch. A runner that cannot be watched makes every
// tick scan all sessions; a revision that cannot be read scans too.
func (s *Service) membersChanged(w *memberWatch, records []room.Record, first bool) bool {
	guard, err := s.membersGuard(records)
	full := w.ticks%MemberFullScanEvery == 0
	w.ticks++
	if first || err != nil || guard != w.guard || w.runners.needsFullScans() {
		w.guard, w.changedSinceFull = guard, true
	} else if !full || !w.changedSinceFull {
		return false
	}
	// A change since the last full scan reaches closed sessions on the
	// next full tick, even when nothing else changes by then.
	w.full = first || full
	if w.full {
		w.changedSinceFull = false
	}
	return true
}

type memberGuard struct {
	revision, owners string
}

// membersGuard captures the members revision and room owners read by a scan.
func (s *Service) membersGuard(records []room.Record) (memberGuard, error) {
	owners := make([][2]string, 0, len(records))
	for _, rec := range records {
		owners = append(owners, [2]string{rec.ID, watchJSON([]any{rec.Name, rec.Seat})})
	}
	revision, err := s.sessions().MembersRevision()
	return memberGuard{revision: revision, owners: watchJSON(owners)}, err
}

// scanDirtyMembers scans the sessions named since the last wake, against
// the sessions the last scan read, and every session after SessionsChanged.
// A targeted scan stores the captured guard only when its shared snapshot
// covers every write at that revision. Partial coverage retains fallback.
func (s *Service) scanDirtyMembers(w *memberWatch, emit func(Event)) {
	ids, all := s.takeDirty()
	if !all && len(ids) == 0 {
		return
	}
	records, err := s.rooms().List(true)
	if err != nil {
		return
	}
	guard, guardErr := s.membersGuard(records)
	var sessions []subagent.Info
	if all || w.sessions == nil {
		ids = nil
		if sessions, err = w.list(s); err != nil {
			return
		}
	} else {
		sessions, err = w.refresh(s, ids)
		if err != nil {
			return
		}
	}
	if s.onMemberScan != nil {
		if ids == nil {
			s.onMemberScan(-1)
		} else {
			s.onMemberScan(len(sessions))
		}
	}
	s.scanMembers(sessions, records, w, ids, false, emit)
	w.runners.follow(w.sessions)
	if guardErr == nil && (ids == nil || w.view != nil && w.view.revision == guard.revision) {
		w.guard = guard
	}
}

// newRunners watches the runners of running sessions, and checks each one
// against the launch records of this data folder.
func (s *Service) newRunners() *runnerWatch {
	dataDir := filepath.Dir(s.sessions().Root)
	recorded := func(p callerid.Process) (callerid.Launched, bool, error) {
		store, err := callerid.OpenStore(dataDir)
		if err != nil {
			return callerid.Launched{}, false, err
		}
		return store.ProcessLaunch(p)
	}
	w := newRunnerWatch(s.SessionsChanged, recorded)
	w.exited = s.RunnerExited
	w.dataDir = dataDir
	w.coordinatorExited = s.CoordinatorExited
	return w
}
