package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"dyna-agent/internal/pr/links"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
)

// Responsibility is one thing a session owns. A session may finish only when
// it owns none. Close hands work to an ancestor and refuses failed handoffs unless forced.
type Responsibility = sessionclose.Responsibility

// owned is one responsibility and the session that owns it.
type owned struct {
	info Info
	r    Responsibility
}

// Responsibilities lists everything the session owns. Each store is read under
// its publication lock, so a check serializes with new assignments. The goal
// and question stores take that lock in their own locked reads.
func (m *Manager) Responsibilities(ctx context.Context, info Info) ([]Responsibility, error) {
	items, err := m.ownedBy(ctx, []Info{info}, true)
	if err != nil {
		return nil, err
	}
	out := make([]Responsibility, len(items))
	for i, item := range items {
		out[i] = item.r
	}
	return out, nil
}

// ResponsibilityCounts counts, by kind, what each of the given sessions owns.
// Each store is read once for all of them, so a room's member list costs one
// pass instead of one pass for every session. The kinds and their ownership
// rules are the ones Responsibilities uses: both read the same collectors.
// The store collectors are served from the count cache while their revisions
// hold. Shells are read on every call, because a shell has no revision.
func (m *Manager) ResponsibilityCounts(ctx context.Context, infos []Info) (map[string]map[string]int, error) {
	key, err := m.countKey()
	if err != nil {
		return nil, err
	}
	cache := m.countCache()
	cached, missing := cache.lookup(key, infos)
	var fresh []owned
	if len(missing) > 0 {
		fresh, err = m.cachedOwned(ctx, missing, false)
		if err == nil {
			cache.store(key, missing, fresh)
		}
	}
	shells, shellErr := m.shellsOwned(ctx, infos)
	return countsOf(append(append(cached, fresh...), shells...)), errors.Join(err, shellErr)
}

// countKey names the revisions that a count depends on. It reads every
// revision before any collector runs. A write that lands during a count then
// leaves a newer revision than the key, so the next count reads again.
func (m *Manager) countKey() (string, error) {
	pr, err := state.Revision(state.Root(filepath.Dir(m.store().root())))
	if err != nil {
		return "", err
	}
	goals, err := m.goalStore().Revision()
	if err != nil {
		return "", err
	}
	items, err := m.goalStore().ItemsRevision()
	if err != nil {
		return "", err
	}
	questions, err := m.questionStore().Revision()
	if err != nil {
		return "", err
	}
	runs, err := runstore.Revision()
	if err != nil {
		return "", err
	}
	sessions, err := sessionlife.CacheRevision(m.store().root())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pr=%s goals=%s items=%s questions=%s runs=%d sessions=%s", pr, goals, items, questions, runs, sessions), nil
}

// countCache keeps the store items that each session owns, for one revision
// key. A new key empties it, so a count never mixes two revisions.
type countCache struct {
	mu        sync.Mutex
	key       string
	bySession map[string][]owned
	// recounts counts the reads stored. A test reads it to see a reuse.
	recounts int
}

// countCache returns the manager's count cache. The package lock guards only
// the first allocation, so a Manager copied after that shares the cache.
var countCacheInit sync.Mutex

func (m *Manager) countCache() *countCache {
	countCacheInit.Lock()
	defer countCacheInit.Unlock()
	if m.counts == nil {
		m.counts = &countCache{}
	}
	return m.counts
}

// lookup returns the cached items of infos under key, and the infos that the
// cache lacks. A new key empties the cache first.
func (c *countCache) lookup(key string, infos []Info) (cached []owned, missing []Info) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != key {
		c.key = key
		c.bySession = map[string][]owned{}
	}
	for _, info := range infos {
		items, ok := c.bySession[info.ID]
		if !ok {
			missing = append(missing, info)
		}
		cached = append(cached, items...)
	}
	return cached, missing
}

// store keeps the items that a read of missing found. It skips the store when
// another key has replaced this one, because the read may be stale.
func (c *countCache) store(key string, missing []Info, items []owned) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != key {
		return
	}
	c.recounts++
	for _, info := range missing {
		c.bySession[info.ID] = nil
	}
	for _, item := range items {
		c.bySession[item.info.ID] = append(c.bySession[item.info.ID], item)
	}
}

// countsOf groups the owned items by session and kind.
func countsOf(items []owned) map[string]map[string]int {
	counts := make(map[string]map[string]int)
	for _, item := range items {
		byKind := counts[item.info.ID]
		if byKind == nil {
			byKind = map[string]int{}
			counts[item.info.ID] = byKind
		}
		byKind[string(item.r.Kind)]++
	}
	return counts
}

// ownedBy reads each store once and returns what infos own, with the error of
// any store that could not be read. A caller that decides whether a session may
// close must refuse on that error; a count may use what was read. Only the close
// path repairs a session whose turn runner is gone (repair), because a web read
// must not change a session or a room file.
func (m *Manager) ownedBy(ctx context.Context, infos []Info, repair bool) ([]owned, error) {
	items, err := m.cachedOwned(ctx, infos, repair)
	shells, shellErr := m.shellsOwned(ctx, infos)
	return append(items, shells...), errors.Join(err, shellErr)
}

// cachedOwned reads the store collectors that the count cache may keep. It
// never reads shells, which have no revision.
func (m *Manager) cachedOwned(ctx context.Context, infos []Info, repair bool) ([]owned, error) {
	byID := byIDOf(infos)
	base := filepath.Dir(m.store().root())
	var out []owned
	collect := func(list func() ([]owned, error)) func() error {
		return func() error {
			items, err := list()
			out = append(out, items...)
			return err
		}
	}
	err := errors.Join(
		sessionlife.WithPublication(ctx, state.Root(base), "state.lock", collect(func() ([]owned, error) { return m.prOwned(byID, repair) })),
		collect(func() ([]owned, error) { return m.goalOwned(byID) })(),
		collect(func() ([]owned, error) { return m.itemOwned(byID) })(),
		collect(func() ([]owned, error) { return m.questionOwned(byID) })(),
		sessionlife.WithPublication(ctx, filepath.Join(base, "runs"), "publication.lock", collect(func() ([]owned, error) { return m.runOwned(byID) })),
		collect(func() ([]owned, error) { return m.childOwned(byID, repair) })(),
	)
	return out, err
}

// shellsOwned reads the live shell jobs under the shells publication lock.
func (m *Manager) shellsOwned(ctx context.Context, infos []Info) ([]owned, error) {
	var out []owned
	err := sessionlife.WithPublication(ctx, filepath.Join(filepath.Dir(m.store().root()), "shells"), "publication.lock", func() error {
		items, err := m.shellOwned(byIDOf(infos))
		out = items
		return err
	})
	return out, err
}

// byIDOf indexes the sessions by ID.
func byIDOf(infos []Info) map[string]Info {
	byID := make(map[string]Info, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
	return byID
}

func responsibilityError(verb string, items []Responsibility) error {
	lines := make([]string, len(items))
	for i, r := range items {
		lines[i] = r.Text
	}
	return fmt.Errorf("responsibilities: cannot %s:\n- %s", verb, strings.Join(lines, "\n- "))
}

// byRoom groups the sessions by room, so a store read per room is read once
// and not once for every session.
func byRoom(byID map[string]Info) map[string][]Info {
	out := map[string][]Info{}
	for _, info := range byID {
		out[info.RoomID] = append(out[info.RoomID], info)
	}
	return out
}

// prOwned reads only the rows linked to the given sessions in each room. A
// link that is not active owns the session nothing. Only the close path
// (repair) reads the store fresh; a count uses the shared snapshot.
func (m *Manager) prOwned(byID map[string]Info, repair bool) ([]owned, error) {
	root := state.Root(filepath.Dir(m.store().root()))
	read := state.ReadForSessions
	if repair {
		read = state.ReadForSessionsFresh
	}
	var out []owned
	var errs []error
	for roomID, infos := range byRoom(byID) {
		owners := make(map[string]Info, len(infos))
		ids := make([]string, 0, len(infos))
		for _, info := range infos {
			owners[info.ID] = info
			ids = append(ids, info.ID)
		}
		st, err := read(root, roomID, ids...)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, roleLink := range links.RoleLinks(st, roomID) {
			info, ok := owners[roleLink.Session]
			if !ok || !roleLink.Active() {
				continue
			}
			for _, role := range roleLink.Roles {
				r := Responsibility{Kind: "pr", ID: roleLink.Key}
				switch {
				case roleLink.Kind == "local":
					r.Text = fmt.Sprintf("%s is change %s %s; stop tracking with: dyna git change close %s", info.Address(), roleLink.Key, role, roleLink.Key)
				case role == "reviewer":
					r.Text = fmt.Sprintf("%s reviews PR #%d %s; reset the managed reviewer with: dyna git pr reviewer --room %s --restart; stop tracking: dyna git pr untrack %s", info.Address(), roleLink.Number, roleLink.URL, info.Room, roleLink.URL)
				default:
					r.Text = fmt.Sprintf("%s is PR #%d %s %s; transfer: dyna git pr transfer %s --role %s --to <session>; stop tracking: dyna git pr untrack %s", info.Address(), roleLink.Number, roleLink.URL, role, roleLink.URL, role, roleLink.URL)
				}
				out = append(out, owned{info: info, r: r})
			}
		}
	}
	return out, errors.Join(errs...)
}

func (m *Manager) goalOwned(byID map[string]Info) ([]owned, error) {
	var out []owned
	var errs []error
	for roomID, infos := range byRoom(byID) {
		goals, err := m.goalStore().ListOpen(roomID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, g := range goals {
			for _, info := range infos {
				if !g.HeldBy(info.ID) {
					continue
				}
				out = append(out, owned{info: info, r: Responsibility{Kind: "goal", ID: g.ID, Text: fmt.Sprintf("%s holds goal %s; transfer: dyna goal edit %s --for @<session>; finish: dyna goal done %s \"completed\"; cancel: dyna goal drop %s", info.Address(), g.ID, g.ID, g.ID, g.ID)}})
			}
		}
	}
	return out, errors.Join(errs...)
}

// itemOwned lists the goal items assigned to the sessions. An item is a
// responsibility of its assignee, who does not hold the goal it belongs to.
func (m *Manager) itemOwned(byID map[string]Info) ([]owned, error) {
	var out []owned
	var errs []error
	for roomID, infos := range byRoom(byID) {
		owners := make(map[string]Info, len(infos))
		for _, info := range infos {
			owners[info.ID] = info
		}
		assigned, err := m.goalStore().OpenItemsIn(roomID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, a := range assigned {
			info, ok := owners[a.Item.Assignee]
			if !ok {
				continue
			}
			out = append(out, owned{info: info, r: Responsibility{Kind: "item", ID: a.Goal + "/" + a.Item.ID, Text: fmt.Sprintf("%s is assigned item %s on goal %s; finish: dyna goal item done %s %s; hand back: dyna goal item edit %s %s --for none", info.Address(), a.Item.ID, a.Goal, a.Goal, a.Item.ID, a.Goal, a.Item.ID)}})
		}
	}
	return out, errors.Join(errs...)
}

func (m *Manager) questionOwned(byID map[string]Info) ([]owned, error) {
	var out []owned
	var errs []error
	for roomID, infos := range byRoom(byID) {
		questions, err := m.questionStore().ListOpen(roomID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, q := range questions {
			for _, info := range infos {
				if q.Asker != info.ID {
					continue
				}
				out = append(out, owned{info: info, r: Responsibility{Kind: "question", ID: q.ID, Text: fmt.Sprintf("%s awaits question %s; cancel: dyna request withdraw %s --why \"no longer needed\"", info.Address(), q.ID, q.ID)}})
			}
		}
	}
	return out, errors.Join(errs...)
}

// childOwned lists open children that have not finished, from one scan of the
// session list. A count reads the list without repairing it, so a member row
// never changes a session.
func (m *Manager) childOwned(byID map[string]Info, repair bool) ([]owned, error) {
	list := func() ([]Info, error) { return m.store().ListSnapshot(false) }
	if repair {
		list = func() ([]Info, error) { return m.store().List(false) }
	}
	all, err := list()
	if err != nil {
		return nil, err
	}
	var out []owned
	for _, child := range all {
		parent, ok := byID[child.ParentSession]
		if !ok || child.RoomID != parent.RoomID || child.Status == StatusDormant {
			continue
		}
		out = append(out, owned{info: parent, r: Responsibility{Kind: "child", ID: child.ID, Text: fmt.Sprintf("%s has an unfinished child %s; ask it to finish, or close it: dyna subagent close %s", parent.Address(), child.Address(), child.ID)}})
	}
	return out, nil
}

// shellOwned lists live shell jobs the sessions own, from one list of them all.
func (m *Manager) shellOwned(byID map[string]Info) ([]owned, error) {
	root := filepath.Join(filepath.Dir(m.store().root()), "shells")
	list, err := (&shells.Store{Root: root}).List(false)
	var out []owned
	// The job that runs this process ends with it.
	own := os.Getenv(shells.JobEnv)
	for _, job := range list {
		info, ok := byID[job.Owner]
		if !ok || job.ID == own {
			continue
		}
		out = append(out, owned{info: info, r: Responsibility{Kind: "shell", ID: job.ID, Text: "owns live shell job " + job.ID + "; stop it: dyna shell stop " + job.ID}})
	}
	return out, err
}

// runOwned lists running workflow runs the sessions own, from one read of the
// runs directory.
func (m *Manager) runOwned(byID map[string]Info) ([]owned, error) {
	root := filepath.Join(filepath.Dir(m.store().root()), "runs")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []owned
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if runstore.ValidateRunID(entry.Name()) != nil {
			continue
		}
		job, err := runstore.ReadMetaIn(root, entry.Name())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				unpublished, inspectErr := unpublishedDormantJob(root, entry.Name())
				if inspectErr != nil {
					return nil, inspectErr
				}
				if unpublished {
					continue
				}
			}
			return nil, err
		}
		if job.ID != entry.Name() {
			return nil, fmt.Errorf("workflow %s has invalid published identity", entry.Name())
		}
		switch job.Status {
		case "running", "ok", "error", "canceled", runstore.StatusInterrupted:
		default:
			return nil, fmt.Errorf("workflow %s has invalid published status", entry.Name())
		}
		if job.Status != "running" {
			continue
		}
		owner := job.ParentSession
		if owner == "" {
			owner = job.Session
		}
		if info, ok := byID[owner]; ok {
			out = append(out, owned{info: info, r: Responsibility{Kind: "workflow", ID: job.ID, Text: "owns live workflow run " + job.ID + "; cancel it: dyna runs cancel " + job.ID}})
		}
	}
	return out, nil
}

// handOverChildren runs under the names lock after a session closes. It
// returns the children that own nothing, to close next. Their own children get
// the same choice when they close, so grandchildren do not count here. The rest
// move to the heir, or become top-level leads when there is no open ancestor.
func (m *Manager) handOverChildren(ctx context.Context, closed Meta, heir Info) (closeNext []string, summary []sessionclose.Outcome, err error) {
	children, err := m.OpenChildren(closed.ID)
	if err != nil {
		return nil, nil, err
	}
	parent, depth := "", 1
	if heir.ID != "" {
		parent, depth = heir.ID, heir.Depth+1
	}
	for _, child := range children {
		items, err := m.Responsibilities(ctx, child)
		if err != nil {
			return closeNext, summary, err
		}
		items = slices.DeleteFunc(items, func(r Responsibility) bool { return r.Kind == "child" })
		if len(items) == 0 {
			closeNext = append(closeNext, child.ID)
			summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityChild, Result: sessionclose.ResultClosed, ID: child.ID, Name: child.Name})
		} else if err = m.reparentLockedContext(ctx, child.ID, parent, depth); err != nil {
			// The child names the failure itself: the client can then offer to
			// move it, which needs its name.
			summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityChild, Result: sessionclose.ResultFailed, ID: child.ID, Name: child.Name, Failure: sessionclose.FailureChildHandoff})
			return closeNext, summary, err
		} else {
			summary = append(summary, sessionclose.Outcome{Kind: sessionclose.ResponsibilityChild, Result: sessionclose.ResultMoved, ID: child.ID, Name: child.Name, To: heirAddress(heir)})
		}
	}
	return closeNext, summary, nil
}
