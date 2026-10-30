package roomsvc

import (
	"maps"
	"slices"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/listpage"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/terminalsvc"
)

// shellJobWatch is the room watcher's copy of every shell job row. The shell
// store sends an event for each job that changes, so a scan reads only those
// jobs, not the store (thousands of jobs) or any job folder.
type shellJobWatch struct {
	events  *shells.Events // nil until opened; reopened after lost events
	jobs    map[string]shellJobRow
	watched map[string]bool            // jobs whose row can change with no event
	heads   map[string]map[string]bool // room ID → IDs on its first page
	expires map[string]time.Time       // room ID → when a first-page item leaves it by age
	// room ID → IDs of open jobs and closed jobs within listpage.Recent; only these can reach a first page
	candidates map[string]map[string]bool
	sigs       map[string]string // listchange snapshot
}

type shellJobRow struct {
	info shells.Info
	row  terminalsvc.ShellJob
}

func newShellJobWatch() *shellJobWatch {
	return &shellJobWatch{jobs: map[string]shellJobRow{}, watched: map[string]bool{},
		heads: map[string]map[string]bool{}, expires: map[string]time.Time{}, candidates: map[string]map[string]bool{}, sigs: map[string]string{}}
}

func (w *shellJobWatch) close() {
	if w.events != nil {
		w.events.Close()
		w.events = nil
	}
}

// needsWatch reports whether a job's row can change with no event: a running
// job's supervisor can die without a write, a starting one may never start,
// and an ended job's tail ages out.
func needsWatch(in shells.Info) bool {
	return in.Status == "running" || in.Status == "starting" || (in.Exit != nil && terminalsvc.ShellJobLive(in))
}

// due reports whether a watched job must be read again now.
func due(in shells.Info) bool {
	switch in.Status {
	case "running":
		return !in.SupervisorAlive()
	case "starting":
		return true
	}
	return !terminalsvc.ShellJobLive(in)
}

// scanShellJobs reads the room watcher w from the shell store of the session root.
func (s *Service) scanShellJobs(w *shellJobWatch, first bool, emit func(Event)) {
	w.scan(terminalsvc.ShellStore(s.sessions().Root), first, emit)
}

// scan publishes the job events for the rows that changed in store since the last scan.
func (w *shellJobWatch) scan(store *shells.Store, first bool, emit func(Event)) {
	var ids map[string]bool
	if w.events != nil {
		var err error
		if ids, err = w.events.Read(); err != nil {
			w.close()
		}
	}
	full := first || w.events == nil
	if w.events == nil {
		// Watch first, so a change during the full read below is not missed.
		// Without events every scan is a full read.
		w.events, _ = store.WatchEvents()
	}
	var changed []shells.Info
	var err error
	if full {
		changed, err = listShellJobs(store)
	} else {
		for id := range w.watched {
			if due(w.jobs[id].info) {
				ids[id] = true
			}
		}
		changed, err = readShellJobs(store, slices.Collect(maps.Keys(ids)))
	}
	if err != nil {
		w.close()
		return
	}
	if full {
		clear(w.jobs)
		clear(w.watched)
		clear(w.heads)
		clear(w.expires)
		clear(w.candidates)
	}
	touched, rooms := map[string]bool{}, map[string]bool{}
	now := terminalsvc.Clock()
	cutoff := now.Add(-listpage.Recent)
	for _, in := range changed {
		job := shellJobRow{info: in, row: buildShellJobRow(store, in)}
		old, seen := w.jobs[in.ID]
		if seen && old.info.RoomID != in.RoomID {
			// Rare: only a full Sync reports the row gone from its old room.
			full = true
			delete(w.candidates[old.info.RoomID], in.ID)
		}
		if a, b := terminalsvc.ShellJobMeta(old.row), terminalsvc.ShellJobMeta(job.row); !seen || a.Open != b.Open || !a.At.Equal(b.At) {
			rooms[in.RoomID] = true
		}
		w.jobs[in.ID] = job
		touched[in.ID] = true
		if needsWatch(in) {
			w.watched[in.ID] = true
		} else {
			delete(w.watched, in.ID)
		}
		if m := terminalsvc.ShellJobMeta(job.row); in.RoomID != "" && (m.Open || !m.At.Before(cutoff)) {
			if w.candidates[in.RoomID] == nil {
				w.candidates[in.RoomID] = map[string]bool{}
			}
			w.candidates[in.RoomID][in.ID] = true
		} else {
			delete(w.candidates[in.RoomID], in.ID)
		}
	}
	// A finished job also leaves a first page by age alone, with no write.
	for room, at := range w.expires {
		if !now.Before(at) {
			rooms[room] = true
		}
	}
	delete(rooms, "")
	if full {
		for room := range w.candidates {
			rooms[room] = true
		}
	}
	// A room's first page changes only when one of its jobs starts, ends or
	// ages out; rows that enter or leave it are reported with the touched ones.
	for room := range rooms {
		cands := w.candidates[room]
		items := make([]listpage.Meta, 0, len(cands))
		for id := range cands {
			m := terminalsvc.ShellJobMeta(w.jobs[id].row)
			if !m.Open && m.At.Before(cutoff) {
				// Closed before the window: it never shows on a first page again.
				delete(cands, id)
				continue
			}
			items = append(items, m)
		}
		page, err := pageShellJobs(items)
		if err != nil {
			w.close()
			return
		}
		head := listchange.HeadIDs(page.Items, func(job listpage.Meta) string { return job.ID })
		for id := range w.heads[room] {
			touched[id] = true
		}
		maps.Copy(touched, head)
		w.heads[room] = head
		// A finished job leaves a first page by age alone, with no write.
		var expires time.Time
		for _, m := range page.Items {
			if m.Open {
				continue
			}
			if at := m.At.Add(listpage.Recent); expires.IsZero() || at.Before(expires) {
				expires = at
			}
		}
		if expires.IsZero() {
			delete(w.expires, room)
		} else {
			w.expires[room] = expires
		}
	}
	var rows []listchange.Item
	add := func(job shellJobRow) {
		if room := job.info.RoomID; room != "" {
			inHead := w.heads[room][job.info.ID]
			rows = append(rows, listchange.Item{Kind: "jobs", RoomID: room, ID: job.info.ID, Value: job.row, InHead: &inHead})
		}
	}
	var changes []listchange.Event
	if full {
		for _, job := range w.jobs {
			add(job)
		}
		changes, err = listchange.Sync(w.sigs, rows, first)
	} else {
		for id := range touched {
			add(w.jobs[id])
		}
		changes, err = listchange.Update(w.sigs, rows)
	}
	if err != nil {
		w.close()
		return
	}
	for _, change := range changes {
		emit(Event{Kind: "jobs", RoomID: change.RoomID, Change: &change.Change})
	}
}

// Tests count the actual store reads, row work and page builds. These seams
// leave the existing event, recovery and expiry gates unchanged.
var listShellJobs = func(store *shells.Store) ([]shells.Info, error) { return store.List(true) }
var readShellJobs = func(store *shells.Store, ids []string) ([]shells.Info, error) { return store.Infos(ids) }
var buildShellJobRow = terminalsvc.ShellJobRow
var pageShellJobs = func(items []listpage.Meta) (listpage.Page[listpage.Meta], error) {
	return listpage.Select(items, "", 0, terminalsvc.Clock(), func(job listpage.Meta) listpage.Meta { return job })
}
