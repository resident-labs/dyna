package roomsvc

import (
	"encoding/json"
	"sort"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

type memberVersion struct {
	room, member, controls, consolidations, session, turn, turnState string
	closed                                                           bool
}

type memberRoom struct {
	owner string
	items []Member
}
type memberWatch struct {
	versions map[string]memberVersion
	rooms    map[string]memberRoom
	rows     map[string]string
	// Closed sessions get new signatures only on full scans, every
	// MemberFullScanEvery ticks, and only when something changed since the
	// last full scan (membersChanged).
	ticks            int
	full             bool
	changedSinceFull bool
	guard            memberGuard // the members revision and room owners covered by the last scan
	runners          *runnerWatch
	// sessions is what the last scan read, newest first, as ListSnapshot
	// orders it; a wake reads again only the sessions it names.
	sessions  []subagent.Info
	exits     uint64          // runner exit epoch covered by the last full roster
	positions map[string]int  // positions in this watch's private roster
	view      *rosterSnapshot // shared only while this watch covers all of its writes
}

// refresh updates the cached roster and returns only the sessions read
// again for ids. A session that no longer reads is gone from the roster.
func (w *memberWatch) refresh(s *Service, ids map[string]bool) ([]subagent.Info, error) {
	view, err := s.readRoster(s.sessions().Info, false)
	if err != nil {
		return nil, err
	}
	if w.positions == nil {
		w.indexSessions()
	}
	inheritance := map[string]bool{}
	for id := range ids {
		before, existed := w.positions[id]
		after, present := view.positions[id]
		if existed != present || existed && serviceInheritanceChanged(w.sessions[before], view.all[after]) {
			inheritance[id] = true
		}
	}
	// Only descendants of this wake's changed ancestors join the scan.
	// Other unwoken writes keep their separate fallback coverage.
	includeMemberDescendants(view.all, inheritance)
	for id := range inheritance {
		ids[id] = true
	}
	if w.view != view {
		// Sharing a rendered tree requires coverage of every write it holds.
		// An unwoken B must still wait for the full scan after a wake of A.
		covered := !view.reset && w.view != nil && w.view.revision == view.previous && w.view.exits == view.exits
		for _, id := range view.changed {
			covered = covered && ids[id]
		}
		if covered {
			w.view = view
		} else {
			w.view = nil
		}
	}
	all, current := view.all, view.positions
	fresh := make([]subagent.Info, 0, len(ids))
	reorder := false
	for id := range ids {
		positionInSnapshot, present := current[id]
		var info subagent.Info
		if present {
			info = all[positionInSnapshot]
		}
		position, exists := w.positions[id]
		if !present {
			if exists {
				copy(w.sessions[position:], w.sessions[position+1:])
				last := len(w.sessions) - 1
				w.sessions[last] = subagent.Info{}
				w.sessions = w.sessions[:last]
				delete(w.positions, id)
				for i := position; i < len(w.sessions); i++ {
					w.positions[w.sessions[i].ID] = i
				}
			}
			continue
		}
		fresh = append(fresh, info)
		if exists {
			reorder = reorder || !w.sessions[position].Created.Equal(info.Created)
			w.sessions[position] = info
		} else {
			w.positions[id] = len(w.sessions)
			w.sessions = append(w.sessions, info)
			reorder = true
		}
	}
	if reorder {
		sortMemberSessions(w.sessions)
		w.indexSessions()
	}
	if len(fresh) > 1 {
		sortMemberSessions(fresh)
	}
	return fresh, nil
}

func (w *memberWatch) indexSessions() {
	w.positions = make(map[string]int, len(w.sessions))
	for i := range w.sessions {
		w.positions[w.sessions[i].ID] = i
	}
}

// list reads the shared session snapshot, so watches list records only when
// the session revision changes.
func (w *memberWatch) list(s *Service) ([]subagent.Info, error) {
	force := w.sessions == nil || w.exits != s.snap.exits.Load() || w.runners != nil && w.runners.needsFullScans()
	view, err := s.readRoster(s.sessions().Info, force)
	if err != nil {
		return nil, err
	}
	w.view, w.exits = view, view.exits
	// refresh mutates this watch's roster. Retained request snapshots stay immutable.
	return copyMemberRoster(view.all), nil
}

func newMemberWatch() *memberWatch {
	return &memberWatch{versions: map[string]memberVersion{}, rooms: map[string]memberRoom{}, rows: map[string]string{}}
}

func watchJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// Transcript output changes independently of roster, control, and consolidation state.
// scanMembers emits the member and session changes in sessions. With only,
// sessions contains only the refreshed records; unrelated cached versions and
// the full roster stay in place.
func (s *Service) scanMembers(sessions []subagent.Info, records []room.Record, state *memberWatch, only map[string]bool, first bool, emit func(Event)) {
	if only == nil {
		state.sessions = sessions
		if state.view != nil && len(sessions) != len(state.view.all) {
			state.view = nil
		}
		state.indexSessions()
	}
	previous := state.versions
	pending := []Event{}
	collect := func(event Event) { pending = append(pending, event) }
	next := previous
	var seen map[string]bool
	if only == nil {
		next = make(map[string]memberVersion, len(sessions))
	} else {
		seen = make(map[string]bool, len(only))
	}
	for _, info := range sessions {
		if only != nil {
			if !only[info.ID] {
				continue
			}
			seen[info.ID] = true
		}
		old, exists := previous[info.ID]
		if only == nil && !state.full && exists && old.closed && info.Status == subagent.StatusClosed {
			next[info.ID] = old
			continue
		}
		// info was read this scan, so its turn count is current, and its record
		// names the delivery keys, so the queue signature needs no address read.
		queue := s.sessions().QueueSignatureOf(info.Meta)
		session := queue + s.sessions().TurnSignature(info.ID, info.Turns)
		turnVersion, turnState := session, old.turnState
		// A turn write can change held work or queue delivery without changing member status.
		if info.Turns > 0 && (!exists || old.turn != session) {
			if turn, err := s.sessions().ReadTurn(info.ID, info.Turns); err == nil {
				steering := make([]any, 0, len(turn.Steering))
				for _, item := range turn.Steering {
					steering = append(steering, []any{item.ID, item.TS, item.Delivered})
				}
				turnState = watchJSON([]any{turn.Status, turn.RoomHeld, turn.HeldPrompt != "", turn.HeldAccepted, steering})
			} else {
				turnVersion = old.turn
			}
		}
		member := watchJSON([]any{info.ID, info.Name, info.RoomID, info.Role, info.Service, info.Status, info.Turns, info.Profile, info.Model, info.ParentSession, info.Depth, info.Closed, info.Finished, info.Resumable, info.HarnessSession, info.ConsumedBy, info.Consolidation, info.Transfer}) + queue + turnState
		value := memberVersion{
			room:           info.RoomID,
			member:         member,
			controls:       watchJSON([]any{info.Status, info.Turns, info.Closed, info.Finished, queue}) + turnState,
			consolidations: watchJSON([]any{info.Consolidation, info.Transfer}),
			session:        session + member + info.LastActivity.String() + watchJSON([]any{info.InheritedInboxes, info.InheritedReceipts, info.ClosedPosts}),
			turn:           turnVersion, turnState: turnState,
			closed: info.Status == subagent.StatusClosed,
		}
		next[info.ID] = value
		if first {
			continue
		}
		if exists && old.room != value.room {
			collect(Event{Kind: "members", RoomID: old.room, Change: &listchange.Change{Op: "delete", ID: info.ID, Controls: true, Consolidations: true}})
		}
		if !exists || old.room != value.room || old.member != value.member {
			collect(Event{Kind: "members", RoomID: value.room, Change: &listchange.Change{Op: "upsert", ID: info.ID, Controls: old.controls != value.controls, Consolidations: old.consolidations != value.consolidations}})
		}
		if !exists || old.room != value.room || old.session != value.session {
			collect(Event{Kind: "session", RoomID: value.room, Change: &listchange.Change{Op: "upsert", ID: info.ID}})
		}
	}
	removed := func(id string, old memberVersion) {
		if !first {
			collect(Event{Kind: "members", RoomID: old.room, Change: &listchange.Change{Op: "delete", ID: id, Controls: true, Consolidations: true}})
		}
	}
	if only == nil {
		for id, old := range previous {
			if _, exists := next[id]; !exists {
				removed(id, old)
			}
		}
		state.versions = next
	} else {
		for id := range only {
			if old, exists := previous[id]; exists && !seen[id] {
				removed(id, old)
				delete(previous, id)
			}
		}
	}

	s.emitMemberItems(state.sessions, records, state, pending, first, emit)
}

func memberID(member Member) string {
	if member.ID != "" {
		return member.ID
	}
	return member.Address
}

// Rebuild only rooms with member or owner changes. Output-only session events
// retain their cached roster, so streamed text cannot become member updates.
func (s *Service) emitMemberItems(sessions []subagent.Info, records []room.Record, state *memberWatch, pending []Event, first bool, emit func(Event)) {
	affected := map[string]bool{}
	forced := map[string]*listchange.Change{}
	for _, event := range pending {
		if event.Kind == "members" {
			affected[event.RoomID] = true
			forced[event.RoomID+"\x00"+event.Change.ID] = event.Change
		}
	}
	type roomRebuild struct {
		roomID, owner string
		items         []Member
		start         int
	}
	var rebuilds []roomRebuild
	var allItems []Member
	present := map[string]bool{}
	for _, rec := range records {
		present[rec.ID] = true
		owner := watchJSON([]any{rec.Name, rec.Seat})
		old, exists := state.rooms[rec.ID]
		if first || !exists || old.owner != owner {
			affected[rec.ID] = true
			if exists && !first && old.owner != owner {
				forced[rec.ID+"\x00"+rec.Name] = &listchange.Change{Controls: true}
			}
		}
		if affected[rec.ID] {
			var items []Member
			if state.view != nil {
				items = s.memberItems(state.view, rec, true)
			} else {
				items = s.membersOf(rec, sessions, "", true)
			}
			start := len(allItems)
			allItems = append(allItems, items...)
			rebuilds = append(rebuilds, roomRebuild{roomID: rec.ID, owner: owner, items: items, start: start})
		}
	}
	// Count every affected room in one responsibility-count call. A streamed row
	// keeps its count, so patching one into the list does not drop it. A goal, question,
	// PR, shell or run change reloads the list, which reads the counts again.
	s.countResponsibilities(allItems, sessions)
	for _, rebuilt := range rebuilds {
		copy(rebuilt.items, allItems[rebuilt.start:rebuilt.start+len(rebuilt.items)])
		state.rooms[rebuilt.roomID] = memberRoom{owner: rebuilt.owner, items: rebuilt.items}
	}
	changed := len(affected) > 0
	for roomID := range state.rooms {
		if !present[roomID] {
			delete(state.rooms, roomID)
			changed = true
		}
	}
	if changed || first {
		rows := []listchange.Item{}
		items := map[string]Member{}
		for roomID, cache := range state.rooms {
			rows = append(rows, listchange.Rows("members", roomID, cache.items, nil, memberID)...)
			for _, item := range cache.items {
				items[roomID+"\x00"+memberID(item)] = item
			}
		}
		changes, err := listchange.Sync(state.rows, rows, first)
		if err == nil && !first {
			emitted := map[string]bool{}
			for _, event := range changes {
				key := event.RoomID + "\x00" + event.Change.ID
				value := event.Change
				if flags := forced[key]; flags != nil {
					value.Controls = flags.Controls
					value.Consolidations = flags.Consolidations
				}
				emit(Event{Kind: "members", RoomID: event.RoomID, Change: &value})
				emitted[key] = true
			}
			// Queue and consolidation changes may require a control update even when
			// the public member fields have not changed.
			for _, event := range pending {
				if event.Kind != "members" {
					continue
				}
				key := event.RoomID + "\x00" + event.Change.ID
				if emitted[key] {
					continue
				}
				value := *event.Change
				if value.Op == "upsert" {
					item, exists := items[key]
					if !exists {
						continue
					}
					value.Item, _ = json.Marshal(item)
				}
				emit(Event{Kind: "members", RoomID: event.RoomID, Change: &value})
			}
		}
	}
	for _, event := range pending {
		if event.Kind != "members" {
			emit(event)
		}
	}
}

// Tests count actual roster sorting through this function.
var sortMemberSessions = func(in []subagent.Info) {
	sort.SliceStable(in, func(i, j int) bool { return in[i].Created.After(in[j].Created) })
}
