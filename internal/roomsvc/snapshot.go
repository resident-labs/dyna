package roomsvc

import (
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// sessionSnapshot owns the committed roster used by Watch and requests.
// Published rosters are immutable; a changed revision copies their slots once.
// Avatar books depend only on record writes, not turn and queue changes.
type sessionSnapshot struct {
	mu    sync.Mutex
	exits atomic.Uint64
	rev   string
	view  *rosterSnapshot
	books map[string]avatarBook
}

type rosterSnapshot struct {
	revision, records string
	previous          string
	changed           []string
	reset             bool
	exits             uint64
	all               []subagent.Info
	positions         map[string]int
	mu                sync.Mutex
	rooms             map[memberPage][]Member
}

type memberPage struct {
	room, owner string
	closed      bool
}

// snapshot returns every session with current status, without repairing turns.
func (s *Service) snapshot() ([]subagent.Info, string) {
	all, rev, _ := s.readSnapshot(s.sessions().Inspect)
	return all, rev
}

func (s *Service) readSnapshot(read func(string) (subagent.Info, error)) ([]subagent.Info, string, error) {
	view, err := s.readRoster(read, false)
	if err != nil {
		return nil, "", err
	}
	return view.all, view.records, nil
}

// A watch forces open sessions through Info after an unkeyed change or when
// its runner watch cannot observe exits. Requests share its committed result.
func (s *Service) readRoster(read func(string) (subagent.Info, error), force bool) (*rosterSnapshot, error) {
	store, c := s.sessions(), &s.snap
	c.mu.Lock()
	defer c.mu.Unlock()
	exits := c.exits.Load()
	old := c.view
	since := ""
	if old != nil {
		since = old.revision
	}
	revision, ids, reset, err := readMemberChanges(store, since)
	if err != nil {
		return nil, err
	}
	recordRevision := strings.SplitN(revision, "|", 2)[0]
	refresh := force || old != nil && exits != old.exits
	if old != nil && !refresh && revision == old.revision {
		return old, nil
	}
	var all []subagent.Info
	var positions map[string]int
	if reset || old == nil {
		all, err = listSessionSnapshot(store)
		if err != nil {
			return nil, err
		}
		// A cold list already inspected every record at this revision.
		ids = nil
	} else if len(ids) != 0 || refresh {
		all = copyMemberRoster(old.all)
	} else {
		all = old.all
	}
	sharedPositions := old != nil && !reset
	if sharedPositions {
		positions = old.positions
	} else {
		positions = indexSnapshot(all)
	}
	pending := make(map[string]bool, len(ids))
	for _, id := range ids {
		pending[id] = true
	}
	if refresh {
		for _, info := range all {
			if info.Status != subagent.StatusClosed {
				pending[info.ID] = true
			}
		}
	}
	inheritance := map[string]bool{}
	reorder := false
	for id := range pending {
		info, err := readSnapshotSession(read, id)
		position, exists := positions[id]
		if err != nil {
			// Do not advance coverage on a failed read. The next request
			// retries; deletion makes MemberChanges request a full roster.
			return nil, err
		}
		if exists {
			if serviceInheritanceChanged(all[position], info) {
				inheritance[id] = true
			}
			reorder = reorder || !all[position].Created.Equal(info.Created)
			all[position] = info
		} else {
			inheritance[id] = true
			if sharedPositions {
				positions = maps.Clone(positions)
				sharedPositions = false
			}
			positions[id] = len(all)
			all = append(all, info)
			reorder = true
		}
	}
	// A descendant's effective Service can change without its own row moving.
	// Resolve that dependency before publishing the immutable roster.
	includeMemberDescendants(all, inheritance)
	for id := range inheritance {
		if pending[id] {
			continue
		}
		info, err := readSnapshotSession(read, id)
		if err != nil {
			return nil, err
		}
		all[positions[id]] = info
		ids = append(ids, id)
	}
	if reorder {
		sortMemberSessions(all)
		positions = indexSnapshot(all)
	}
	if recordRevision != c.rev {
		c.rev, c.books = recordRevision, map[string]avatarBook{}
	}
	view := &rosterSnapshot{revision: revision, records: recordRevision, previous: since, changed: ids, reset: reset, exits: exits,
		all: all, positions: positions, rooms: map[memberPage][]Member{}}
	c.view = view
	return view, nil
}

// These fields determine whether Service is inherited through an ancestor.
// A profile, turn or queue write leaves the dependency unchanged.
func serviceInheritanceChanged(before, after subagent.Info) bool {
	return before.Service != after.Service || before.Role != after.Role ||
		before.ParentSession != after.ParentSession || before.RoomID != after.RoomID
}

func includeMemberDescendants(all []subagent.Info, ids map[string]bool) {
	if len(ids) == 0 {
		return
	}
	children := map[string][]string{}
	for _, info := range all {
		children[info.ParentSession] = append(children[info.ParentSession], info.ID)
	}
	queue := make([]string, 0, len(ids))
	for id := range ids {
		queue = append(queue, id)
	}
	for i := 0; i < len(queue); i++ {
		for _, id := range children[queue[i]] {
			if !ids[id] {
				ids[id] = true
				queue = append(queue, id)
			}
		}
	}
}

// memberItems shares the tree and its sort, then isolates the public reply.
func (s *Service) memberItems(view *rosterSnapshot, rec room.Record, closed bool) []Member {
	key := memberPage{room: rec.ID, owner: watchJSON([]any{rec.Name, rec.Seat}), closed: closed}
	view.mu.Lock()
	items, exists := view.rooms[key]
	if !exists {
		items = buildMemberItems(s, rec, view.all, view.records, closed)
		view.rooms[key] = items
	}
	out := append([]Member(nil), items...)
	for i := range out {
		if out[i].Seat != nil {
			seat := *out[i].Seat
			out[i].Seat = &seat
		}
	}
	view.mu.Unlock()
	// Output activity is a file stream, independent of the stored roster.
	for i := range out {
		s.memberActivity(&out[i])
	}
	return out
}

// Tests count the real tree construction, including its membership sort.
var buildMemberItems = func(s *Service, rec room.Record, all []subagent.Info, rev string, closed bool) []Member {
	return s.buildMembers(rec, all, rev, closed)
}

func indexSnapshot(all []subagent.Info) map[string]int {
	positions := make(map[string]int, len(all))
	for i := range all {
		positions[all[i].ID] = i
	}
	return positions
}

// avatarBook returns the room's book for the sessions of one snapshot. An
// empty rev (no cache) builds it fresh.
func (s *Service) avatarBook(roomID string, all []subagent.Info, rev string) avatarBook {
	c := &s.snap
	c.mu.Lock()
	defer c.mu.Unlock()
	if rev != "" && rev == c.rev {
		if book, ok := c.books[roomID]; ok {
			return book
		}
	}
	book := newAvatarBook(all, roomID)
	if rev != "" && rev == c.rev {
		c.books[roomID] = book
	}
	return book
}

// Tests count the real list operation through this function.
var listSessionSnapshot = func(store *subagent.Store) ([]subagent.Info, error) { return store.ListSnapshot(true) }

// Tests count the actual record read and roster copy, including watch work.
var readSnapshotSession = func(read func(string) (subagent.Info, error), id string) (subagent.Info, error) {
	return read(id)
}
var copyMemberRoster = func(all []subagent.Info) []subagent.Info {
	return append([]subagent.Info(nil), all...)
}

// Tests count actual cursor reads, including the revision queries they use.
var readMemberChanges = func(store *subagent.Store, since string) (string, []string, bool, error) {
	return store.MemberChanges(since)
}
