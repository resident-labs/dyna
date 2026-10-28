package view

import (
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/listpage"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

// Tests count actual isolated record copies through this function.
var copyProjectionRecord = state.Snapshot.Record

// Tests count actual reviewer-fact observations through the existing reader.
var projectionFacts = (*watch.Service).ReviewFacts

type projectionCache struct {
	mu      sync.Mutex
	exits   atomic.Uint64
	current *projection
}

// A projection owns isolated records and reuses unchanged records from the
// previous projection. Its inputs never change. Only memoized rows and pages
// need a lock. No record points into the shared state snapshot.
type projection struct {
	root, revision, version, members string
	priority                         string
	rooms                            int64
	at                               time.Time
	exits                            uint64
	until                            time.Time
	state                            watch.State
	snapshot                         state.Snapshot
	facts                            watch.ReviewFacts
	mu                               sync.Mutex
	rows                             map[string]ChangeView
	pages                            map[projectionPage]listpage.Page[*watch.Record]
}

type projectionPage struct {
	room, before string
	limit        int
	prs          bool
}

// SessionsChanged invalidates observations, such as a runner exit, which
// have no stored revision. Publication never waits for a projection reader.
func (s *Service) SessionsChanged() { s.projection.exits.Add(1) }

func (s *Service) readProjection() (*projection, error) {
	c := &s.projection
	c.mu.Lock()
	defer c.mu.Unlock()
	watcher := s.Watcher()
	snapshot, err := state.ReadSnapshot(watcher.Store.Root)
	if err != nil {
		return nil, err
	}
	members, err := s.Notify.Sessions.Store.MembersRevision()
	if err != nil {
		return nil, err
	}
	rooms, err := s.Notify.Rooms.RecordsRevision()
	if err != nil {
		return nil, err
	}
	priority, priorityUntil, err := watcher.PriorityInputs()
	if err != nil {
		return nil, err
	}
	exits := c.exits.Load()
	old := c.current
	now := time.Now()
	if watcher.Now != nil {
		now = watcher.Now()
	}
	if old != nil && old.root == watcher.Store.Root && old.revision == snapshot.Revision() && old.members == members && old.rooms == rooms && old.priority == priority && old.exits == exits && (old.until.IsZero() || now.Before(old.until)) {
		return old, nil
	}
	var st watch.State
	if old != nil && old.root == watcher.Store.Root && old.revision == snapshot.Revision() {
		st = old.state
	} else {
		st = copyProjectionState(snapshot, old)
	}
	version := snapshot.Revision()
	var facts watch.ReviewFacts
	if old != nil && old.root == watcher.Store.Root && old.members == members && old.rooms == rooms && old.priority == priority && old.exits == exits &&
		(old.until.IsZero() || now.Before(old.until)) && sameProjectionInputs(old.state, st) {
		// Keep the committed state current, but reuse the same observation.
		// Its session and pause inputs have not changed. Need must point at
		// this projection's records, not a previous projection's poll times.
		facts = old.facts
		facts.State = st
		facts.Need = make([]*watch.Record, len(old.facts.Need))
		for i, record := range old.facts.Need {
			facts.Need[i] = st.Records[record.Key]
		}
		version = old.version
	} else {
		facts, err = projectionFacts(watcher, st)
		if err != nil {
			return nil, err
		}
	}
	p := &projection{root: watcher.Store.Root, revision: snapshot.Revision(), version: version, members: members, rooms: rooms, priority: priority, until: priorityUntil, at: now, exits: exits,
		state: st, snapshot: snapshot, facts: facts, rows: map[string]ChangeView{}, pages: map[projectionPage]listpage.Page[*watch.Record]{}}
	// History pages and reviewer occupancy have time boundaries even when
	// no writer commits another revision.
	for _, r := range st.Records {
		if r.State == "open" || r.ClosedAt.IsZero() {
			continue
		}
		for _, window := range []time.Duration{time.Hour, listpage.Recent} {
			expires := r.ClosedAt.Add(window).Add(time.Nanosecond)
			if expires.After(now) && (p.until.IsZero() || expires.Before(p.until)) {
				p.until = expires
			}
		}
	}
	c.current = p
	return p, nil
}

// copyProjectionState copies metadata and changed records only. Responses belong
// to the poller and never enter the view. Old projections retain their own map.
func copyProjectionState(snapshot state.Snapshot, old *projection) watch.State {
	st := snapshot.Metadata()
	st.Records = map[string]*watch.Record{}
	var previous state.Snapshot
	if old != nil {
		previous = old.snapshot
		for key, record := range old.state.Records {
			st.Records[key] = record
		}
	}
	for _, key := range snapshot.ChangedKeys(previous) {
		if !snapshot.HasRecord(key) {
			delete(st.Records, key)
			continue
		}
		if record, ok := copyProjectionRecord(snapshot, key); ok {
			st.Records[key] = &record
		} else {
			delete(st.Records, key)
		}
	}
	return st
}

func (p *projection) dependency() string {
	return p.root + "\x00" + p.priority + "|" + p.members + "|" + strconv.FormatInt(p.rooms, 10) + "|" + strconv.FormatUint(p.exits, 10) + "|" + p.until.String()
}

func (p *projection) row(r *watch.Record) ChangeView {
	p.mu.Lock()
	defer p.mu.Unlock()
	// This uncached path remains until the canonical CI summary producer lands.
	if r.Kind == "local" {
		return cloneChangeView(buildRow(p.facts, *r))
	}
	row, ok := p.rows[r.Key]
	if !ok {
		row = buildRow(p.facts, *r)
		p.rows[r.Key] = row
	}
	return cloneChangeView(row)
}

// cloneChangeView gives each caller independent mutable fields without a JSON
// round trip through the cached row.
func cloneChangeView(row ChangeView) ChangeView {
	row.RequiredReviewers = slices.Clone(row.RequiredReviewers)
	row.MissingRequiredReviewers = slices.Clone(row.MissingRequiredReviewers)
	if row.QueuePosition != nil {
		position := *row.QueuePosition
		row.QueuePosition = &position
	}
	row.BotReviews = slices.Clone(row.BotReviews)
	row.Checks = slices.Clone(row.Checks)
	row.Overlaps = slices.Clone(row.Overlaps)
	for i := range row.Overlaps {
		row.Overlaps[i].Files = slices.Clone(row.Overlaps[i].Files)
		row.Overlaps[i].Conflicts = slices.Clone(row.Overlaps[i].Conflicts)
	}
	row.Fixes = slices.Clone(row.Fixes)
	return row
}

func (p *projection) page(room, before string, limit int, prs bool) (listpage.Page[*watch.Record], error) {
	key := projectionPage{room: room, before: before, limit: listpage.Limit(limit), prs: prs}
	p.mu.Lock()
	defer p.mu.Unlock()
	if page, ok := p.pages[key]; ok {
		return page, nil
	}
	page, err := pageRecords(p.state, room, before, limit, prs, p.at)
	if err == nil {
		sort.Slice(page.Items, func(i, j int) bool {
			if !page.Items[i].Created.Equal(page.Items[j].Created) {
				return page.Items[i].Created.After(page.Items[j].Created)
			}
			return page.Items[i].Key < page.Items[j].Key
		})
		p.pages[key] = page
	}
	return page, err
}
