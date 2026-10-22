package subagent

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"dyna-agent/internal/room"
)

// InheritedInbox retains the original queue's identity and cursor. Its source
// is stopped permanently after commit, but its append-only history stays put.
type InheritedInbox struct {
	Dir        string   `json:"dir,omitempty"`
	PendingIDs []string `json:"pendingIds,omitempty"`
	Session    string   `json:"session"`
	Address    string   `json:"address"`
	Cursor     int64    `json:"cursor"`
}

// inboxCache belongs to one live gate. It is rebuilt from durable records on
// restart; advancing its cursors never changes the stored inheritance snapshot.
type inboxCache struct {
	origins  map[string]*inheritedInboxCache
	receipts map[string]*inboxReceipts
	turn     int
	// roomEnd is the complete-record offset already read by the gate this poll.
	roomEnd int64
	// decoded counts the room records this cache has decoded, so a test can
	// hold a repeated read to the records added since the last one.
	decoded int
}

type inheritedInboxCache struct {
	snapshot  InheritedInbox
	cursor    int64
	pending   []room.Message
	recovered map[string]bool
}

type inboxReceipts struct {
	through   int
	delivered map[string]bool
	last      string // version of the latest turn's row
}

// inboxReader supplies one read policy to the shared selection cache.
// A readiness check uses canonical rows throughout; mutation callers retain
// their existing store reader until its separate import retirement.
type inboxReader interface {
	ReadMeta(string) (Meta, error)
	readTurnRow(string, int) ([]byte, string, error)
	decodeTurnRecord(string, int, []byte) (Turn, error)
}

// Completed turns are immutable during live delivery. Only the latest turn
// can gain acknowledgements; body and revision come from one row read.
func (c *inboxCache) delivered(reader inboxReader, meta Meta) (map[string]bool, error) {
	if c.receipts == nil {
		c.receipts = map[string]*inboxReceipts{}
	}
	r := c.receipts[meta.ID]
	if r == nil {
		r = &inboxReceipts{delivered: map[string]bool{}}
		c.receipts[meta.ID] = r
	}
	for n := r.through + 1; n <= meta.Turns; n++ {
		raw, version, err := reader.readTurnRow(meta.ID, n)
		if err != nil {
			return nil, err
		}
		if n == meta.Turns && r.last != "" && r.last == version {
			break
		}
		turn, err := reader.decodeTurnRecord(meta.ID, n, raw)
		if err != nil {
			return nil, err
		}
		for _, steer := range turn.Steering {
			if steer.Delivered {
				r.delivered[steer.ID] = true
			}
		}
		if n < meta.Turns {
			r.through = n
		} else {
			r.last = version
		}
	}
	return r.delivered, nil
}

func (c *inboxCache) messages(rooms *room.Store, roomID string, in InheritedInbox) ([]room.Message, error) {
	if c.origins == nil {
		c.origins = map[string]*inheritedInboxCache{}
	}
	origin := c.origins[in.Session]
	fresh := origin == nil || !reflect.DeepEqual(origin.snapshot, in)
	if fresh {
		origin = &inheritedInboxCache{snapshot: in, cursor: in.Cursor, recovered: map[string]bool{}}
	}
	var msgs []room.Message
	next := origin.cursor
	if fresh || origin.cursor < c.roomEnd || c.turn == 0 {
		var err error
		msgs, next, err = rooms.ReadAll(roomID, origin.cursor)
		if err != nil {
			return nil, err
		}
		c.decoded += len(msgs)
	}
	if fresh {
		tailIDs := map[string]bool{}
		for _, msg := range msgs {
			tailIDs[msg.ID] = true
		}
		for _, id := range in.PendingIDs {
			msg, err := rooms.Message(roomID, id)
			if errors.Is(err, room.ErrPostNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
			if !tailIDs[id] {
				origin.recovered[id] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, msg := range origin.pending {
		seen[msg.ID] = true
	}
	for _, msg := range msgs {
		if !seen[msg.ID] {
			origin.pending = append(origin.pending, msg)
			seen[msg.ID] = true
		}
	}
	// Keep room-tail messages before claims recovered from PendingIDs, as in
	// a fresh snapshot, including records that arrive after a recovered claim.
	if len(msgs) > 0 && len(origin.recovered) > 0 {
		sort.SliceStable(origin.pending, func(i, j int) bool {
			return !origin.recovered[origin.pending[i].ID] && origin.recovered[origin.pending[j].ID]
		})
	}
	origin.cursor = next
	c.origins[in.Session] = origin
	return origin.pending, nil
}

// readInbox is the inbox cache of one target session for callers that only
// read pending messages, such as each web request for room controls.
type readInbox struct {
	sync.Mutex
	cache inboxCache
}

// readInboxes keeps a readInbox per store root and target session, so a
// repeated read decodes only the room records added since the last one. It
// relies on the room log being append-only, and on receipts, cancels and
// dismissals only growing, as the live gate's cache does. Nothing rewrites a
// room log today; a change that deletes, archives or compacts one must drop
// these caches. A closed session's entry is dropped when it is next read, and
// the map holds at most maxReadInboxes entries (item 28).
var readInboxes = struct {
	sync.Mutex
	byKey map[string]*readInbox // root + "\x00" + session ID
}{byKey: map[string]*readInbox{}}

// maxReadInboxes bounds the cache. Only live consolidated targets need an
// entry, normally a handful.
const maxReadInboxes = 256

// inboxMessages reads inherited logs with their own cursors. Called with the
// target gate held; source queue files are atomic snapshots, without nesting
// source gate locks. Claims without acknowledgements remain pending.
func (s *Store) inboxMessages(rooms *room.Store, meta Meta, ordinary []room.Message) ([]room.Message, error) {
	if len(meta.InheritedInboxes) == 0 {
		return ordinary, nil
	}
	r := readInboxFor(s.root()+"\x00"+meta.ID, !meta.Closed.IsZero())
	r.Lock()
	defer r.Unlock()
	return s.cachedInboxMessages(rooms, meta, ordinary, &r.cache, s)
}

// readInboxFor returns the cache for key. A closed session gets a fresh one
// and loses its entry; a full map starts over.
func readInboxFor(key string, closed bool) *readInbox {
	readInboxes.Lock()
	defer readInboxes.Unlock()
	if closed {
		delete(readInboxes.byKey, key)
		return &readInbox{}
	}
	r := readInboxes.byKey[key]
	if r == nil {
		if len(readInboxes.byKey) >= maxReadInboxes {
			clear(readInboxes.byKey)
		}
		r = &readInbox{}
		readInboxes.byKey[key] = r
	}
	return r
}

func (s *Store) cachedInboxMessages(rooms *room.Store, meta Meta, ordinary []room.Message, cache *inboxCache, reader inboxReader) ([]room.Message, error) {
	if len(meta.InheritedInboxes) == 0 {
		return ordinary, nil
	}
	// The gate already checked the room log. With no new records or deferred
	// input, there is no need to load queues, source metadata, or receipt files.
	if cache.turn > 0 && len(ordinary) == 0 {
		idle := true
		for _, in := range meta.InheritedInboxes {
			origin := cache.origins[in.Session]
			if origin == nil || len(origin.pending) > 0 || origin.cursor < cache.roomEnd || !reflect.DeepEqual(origin.snapshot, in) {
				idle = false
				break
			}
		}
		if idle {
			return nil, nil
		}
	}
	all := append([]room.Message(nil), ordinary...)
	// Eligibility is per origin. An uncancelled origin can release a shared post
	// even when another origin cancelled it.
	eligible := map[string]bool{}
	targetDir, _ := s.Dir(meta.ID)
	targetQueue, err := readCanonicalQueue(targetDir)
	if err != nil {
		return nil, err
	}
	for _, msg := range ordinary {
		eligible[msg.ID] = !targetQueue.Cancelled[msg.ID]
	}
	targetDelivered, err := cache.delivered(reader, meta)
	if err != nil {
		return nil, err
	}
	for _, in := range meta.InheritedInboxes {
		source, err := reader.ReadMeta(in.Session)
		if err != nil {
			return nil, err
		}
		dir, _ := s.Dir(in.Session)
		q, err := readCanonicalQueue(dir)
		if err != nil {
			return nil, err
		}
		delivered, err := cache.delivered(reader, source)
		if err != nil {
			return nil, err
		}
		msgs, err := cache.messages(rooms, meta.RoomID, in)
		if err != nil {
			return nil, err
		}
		pending := cache.origins[in.Session].pending[:0]
		for _, msg := range msgs {
			if targetDelivered[msg.ID] || targetQueue.Dismissed[msg.ID] || meta.InheritedReceipts[msg.ID] || (cache.turn > 0 && targetQueue.Claimed[msg.ID] == cache.turn) {
				continue
			}
			suppressed := q.Cancelled[msg.ID] || q.Dismissed[msg.ID] || delivered[msg.ID]
			if suppressed {
				// A shared post retains its position even if this origin cannot release
				// it. Another inherited origin may become eligible on a later poll.
				shared := false
				for _, other := range meta.InheritedInboxes {
					if other.Session != in.Session && msg.Addressed(other.Address) {
						shared = true
						break
					}
				}
				if !shared {
					continue
				}
			}
			if source.Transfer != nil && msg.ID == source.Transfer.PostID {
				continue
			}
			if msg.Deliverable() && (msg.SessionIDs[in.Address] == "" || msg.SessionIDs[in.Address] == source.ID) && !source.ClosedPosts[msg.ID] && msg.Addressed(in.Address) && msg.From != in.Address {
				pending = append(pending, msg)
				all = append(all, msg)
				if !suppressed {
					eligible[msg.ID] = true
				}
			}
		}
		clear(msgs[len(pending):])
		cache.origins[in.Session].pending = pending
	}
	out := []room.Message{}
	seen := map[string]bool{}
	for _, msg := range all {
		if !msg.Deliverable() || targetQueue.Dismissed[msg.ID] || seen[msg.ID] || targetDelivered[msg.ID] || meta.InheritedReceipts[msg.ID] || !eligible[msg.ID] {
			continue
		}
		seen[msg.ID] = true
		out = append(out, msg)
	}
	return out, nil
}

// DeliveryTarget follows stored consumption redirects without changing which
// transcript Resolve reads. Aliases never change a source's historical ID.
func (s *Store) DeliveryTarget(ref string) (Info, error) { return s.deliveryTarget(ref, true) }

// InspectDeliveryTarget follows delivery aliases without repair or reporting.
func (s *Store) InspectDeliveryTarget(ref string) (Info, error) { return s.deliveryTarget(ref, false) }

func (s *Store) deliveryTarget(ref string, repair bool) (Info, error) {
	meta, err := s.deliveryMeta(ref)
	if err != nil {
		return Info{}, err
	}
	return s.info(meta.ID, repair)
}

func (s *Store) deliveryMeta(ref string) (Meta, error) {
	meta, err := s.resolveMeta(ref)
	if err != nil {
		return meta, err
	}
	return s.consumedTarget(meta)
}

// consumedTarget follows the consolidation redirects from meta to the session
// that receives its posts. It never repairs a record.
func (s *Store) consumedTarget(meta Meta) (Meta, error) {
	seen := map[string]bool{}
	for meta.ConsumedBy != "" {
		if seen[meta.ID] {
			return Meta{}, errors.New("consolidation redirect cycle")
		}
		seen[meta.ID] = true
		next, err := s.ReadMeta(meta.ConsumedBy)
		if err != nil {
			return Meta{}, err
		}
		if next.RoomID != meta.RoomID {
			return Meta{}, errors.New("cross-room consolidation redirect")
		}
		meta = next
	}
	return meta, nil
}

// consolidationHierarchy creates a complete cycle-free plan before writing.
// The kept session takes the nearest surviving external parent above the
// highest selected ancestor on its original parent chain.
func (m *Manager) consolidationHierarchy(c ConsolidationState) error {
	return m.consolidationHierarchyPlan(c, true)
}

func (m *Manager) consolidationHierarchyPlan(c ConsolidationState, apply bool) error {
	all, err := m.store().ListMeta()
	if err != nil {
		return err
	}
	metas := map[string]Meta{}
	selected := map[string]bool{}
	for _, id := range c.Sessions {
		selected[id] = true
	}
	for _, info := range all {
		if info.RoomID == c.RoomID {
			metas[info.ID] = info
		}
	}
	kept, ok := metas[c.Target]
	if !ok {
		return ErrNotFound
	}
	// Walk through surviving intermediates too: a selected ancestor above one
	// must not turn that intermediary into the kept session's own ancestor.
	chain := []string{}
	seen := map[string]bool{c.Target: true}
	p := kept.ParentSession
	highest := -1
	for p != "" {
		if seen[p] {
			return errors.New("session hierarchy contains a cycle")
		}
		seen[p] = true
		parent, ok := metas[p]
		if !ok || !parent.Closed.IsZero() {
			break
		}
		chain = append(chain, p)
		if selected[p] {
			highest = len(chain) - 1
		}
		p = parent.ParentSession
	}
	newParent := kept.ParentSession
	if highest >= 0 {
		newParent = metas[chain[highest]].ParentSession
	}
	for selected[newParent] {
		newParent = metas[newParent].ParentSession
	}
	if parent, ok := metas[newParent]; newParent != "" && (!ok || !parent.Closed.IsZero()) {
		newParent = ""
	}
	kept.ParentSession = newParent
	kept.Owner = newParent
	metas[c.Target] = kept
	for id, meta := range metas {
		if id != c.Target && !selected[id] && selected[meta.ParentSession] {
			meta.ParentSession = c.Target
			meta.Owner = c.Target
			metas[id] = meta
		}
	}
	depths := map[string]int{}
	visiting := map[string]bool{}
	var depth func(string) (int, error)
	depth = func(id string) (int, error) {
		if d := depths[id]; d > 0 {
			return d, nil
		}
		if visiting[id] {
			return 0, fmt.Errorf("consolidation would create hierarchy cycle at %s", id)
		}
		visiting[id] = true
		meta, ok := metas[id]
		if !ok {
			return 0, nil
		}
		d := 1
		if meta.ParentSession != "" {
			parent, e := depth(meta.ParentSession)
			if e != nil {
				return 0, e
			}
			d = parent + 1
		}
		visiting[id] = false
		depths[id] = d
		return d, nil
	}
	for id := range metas {
		if selected[id] && id != c.Target {
			continue
		}
		if _, err = depth(id); err != nil {
			return err
		}
	}
	if !apply {
		return nil
	}
	for id, plan := range metas {
		if selected[id] && id != c.Target {
			continue
		}
		if _, err = m.store().UpdateMeta(id, func(meta *Meta) {
			meta.Depth = depths[id]
			meta.ParentSession = plan.ParentSession
			meta.Owner = plan.Owner
		}); err != nil {
			return err
		}
	}
	return nil
}

// consolidationManifest preserves task identity and recovery references,
// including sources absorbed through another survivor.
func (m *Manager) consolidationManifest(meta Meta) string {
	if len(meta.InheritedInboxes) == 0 {
		return ""
	}
	return fmt.Sprintf("\n[DYNA CONSOLIDATED TASK REFERENCES]\nThis session owns %d inherited source inboxes. Before acting, read `dyna subagent show %s --json` for inheritedInboxes and review each source with `dyna subagent history <session>` and `dyna subagent output <session> --turn N --transcript`. Original task directories and pending inbox responsibilities stay in those records.\n", len(meta.InheritedInboxes), meta.ID)
}
