package subagent

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"dyna-agent/internal/room"
)

// OpenChildren lists open direct children. Closing a parent never closes them.
func (m *Manager) OpenChildren(ref string) ([]Info, error) {
	info, err := m.store().Resolve(ref)
	if err != nil {
		return nil, err
	}
	all, err := m.store().List(false)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, child := range all {
		if child.ParentSession == info.ID && child.RoomID == info.RoomID {
			out = append(out, child)
		}
	}
	return out, nil
}

// Promote detaches an open session from its parent. Its children, history,
// harness conversation and active turn stay intact. Top-level depth is 1.
// ctx carries the caller, and it cancels the wait for the hierarchy lock.
func (m *Manager) Promote(ctx context.Context, ref string) (Info, error) {
	info, err := m.authorityTarget(ctx, "subagent promote", ref)
	if err != nil {
		return Info{}, err
	}
	store := m.store()
	unlock, err := lockNamesContext(ctx, store)
	if err != nil {
		return Info{}, err
	}
	defer unlock()
	return m.promoteLockedContext(ctx, info.ID)
}

// promoteLockedContext shares the names lock with Start and the final close
// step. It recomputes every descendant from its parent, including closed
// history.
func (m *Manager) promoteLockedContext(ctx context.Context, id string) (Info, error) {
	return m.moveLockedContext(ctx, id, "", 1)
}

// Adopt moves an open session, with its descendants, under another open
// session in the same room. Like Promote, it keeps history, goals, PR roles,
// worktrees and the running turn. Only root, the owner or the session's
// current parent may move it.
// ctx carries the caller, and it cancels the wait for the hierarchy lock.
func (m *Manager) Adopt(ctx context.Context, ref, parentRef string) (Info, error) {
	info, err := m.authorityTarget(ctx, "subagent adopt", ref)
	if err != nil {
		return Info{}, err
	}
	store := m.store()
	parent, err := store.Resolve(parentRef)
	if err != nil {
		return Info{}, err
	}
	unlock, err := lockNamesContext(ctx, store)
	if err != nil {
		return Info{}, err
	}
	defer unlock()
	p, err := store.ReadMeta(parent.ID)
	if err != nil {
		return Info{}, err
	}
	switch {
	case !p.Closed.IsZero() || p.Closing.Active():
		return Info{}, fmt.Errorf("cannot move %s under %s: it is closed", info.Address(), p.Address())
	case p.RoomID != info.RoomID:
		return Info{}, fmt.Errorf("cannot move %s under %s: it is in another room", info.Address(), p.Address())
	case p.Service && p.Role != RoleRoot:
		// A service's helpers are services too, so the move would change the session's kind.
		return Info{}, fmt.Errorf("cannot move %s under %s: it is a service session", info.Address(), p.Address())
	case info.ManagedService():
		// A service is outside the hierarchy. A move would give it a parent,
		// and it would show up as a child of an ordinary session again.
		return Info{}, fmt.Errorf("cannot move %s: a service changes with dyna services, not with a session move", info.Address())
	}
	return m.moveLockedContext(ctx, info.ID, p.ID, p.Depth+1)
}

// moveLockedContext is the one path for promote and adopt: it moves the
// subtree, then tells the old and the new parent in one quiet notice.
func (m *Manager) moveLockedContext(ctx context.Context, id, parent string, depth int) (Info, error) {
	before, err := m.store().ReadMeta(id)
	if err != nil {
		return Info{}, err
	}
	if err := m.reparentLockedContext(ctx, id, parent, depth); err != nil {
		return Info{}, err
	}
	if before.ParentSession != parent {
		m.noteMove(before, parent)
	}
	return m.store().Info(id)
}

// noteMove posts one note to the old and the new parent sessions that are
// open. It does not wake them. The move stands even if the post fails.
func (m *Manager) noteMove(moved Meta, parent string) {
	var to []string
	name := func(id string) string {
		if id == "" {
			return "top level"
		}
		meta, err := m.store().ReadMeta(id)
		if err != nil {
			return id
		}
		if meta.Closed.IsZero() {
			to = append(to, meta.Address())
		}
		return meta.Address()
	}
	event := room.SessionEvent{
		Type:    room.SessionPromoted,
		Session: moved.Address(),
		From:    name(moved.ParentSession),
		To:      name(parent),
	}
	if parent != "" {
		event.Type = room.SessionAdopted
		event.Detail = m.sourceReview(moved, moved.Turns)
	}
	if len(to) == 0 {
		return
	}
	_, _ = m.rooms().PostEvent(moved.RoomID, event, to...)
}

// reparentLockedContext moves a subtree under an open parent while preserving history.
// The caller holds the names lock.
func (m *Manager) reparentLockedContext(ctx context.Context, id, parent string, depth int) error {
	store := m.store()
	root, err := store.ReadMeta(id)
	if err != nil {
		return err
	}
	if root.Closing.Active() {
		return errors.New("session has a dormant close reservation")
	}
	if root.Consolidating != "" {
		return fmt.Errorf("cancel or finish consolidation before promotion")
	}
	if !root.Closed.IsZero() {
		return ErrClosed
	}
	if root.ParentSession == parent && root.Depth == depth {
		return nil
	}
	all, err := store.sessionIDs()
	if err != nil {
		return err
	}
	children := map[string][]string{}
	for _, id := range all {
		if err := ctx.Err(); err != nil {
			return err
		}
		child, err := store.ReadMeta(id)
		if err != nil {
			continue
		}
		if child.RoomID == root.RoomID {
			children[child.ParentSession] = append(children[child.ParentSession], child.ID)
		}
	}
	type change struct {
		id    string
		depth int
	}
	plan := []change{{id, depth}}
	seen := map[string]bool{id: true}
	for i := 0; i < len(plan); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, child := range children[plan[i].id] {
			if seen[child] {
				return fmt.Errorf("cannot move: session hierarchy contains a cycle at %s", child)
			}
			seen[child] = true
			plan = append(plan, change{child, plan[i].depth + 1})
		}
	}
	if seen[parent] {
		return fmt.Errorf("cannot move %s under its own subtree: that would make a cycle", root.Address())
	}
	// Acquire every metadata lock before starting the hierarchy write. A
	// cancelled waiter must not leave half of a subtree at its new depth.
	ids := make([]string, 0, len(plan))
	for _, ch := range plan {
		ids = append(ids, ch.id)
	}
	sort.Strings(ids)
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, sid := range ids {
		unlock, err := store.lockSessionContext(ctx, sid)
		if err != nil {
			return err
		}
		releases = append(releases, unlock)
	}
	max := m.settings().SubagentMaxDepth
	updates := make([]Meta, 0, len(plan))
	for _, ch := range plan {
		meta, err := store.ReadMeta(ch.id)
		if err != nil {
			return err
		}
		// Only a move deeper is refused, so promote and close hand-over always work.
		if max > 0 && ch.depth > max && ch.depth > meta.Depth && meta.Closed.IsZero() {
			return fmt.Errorf("cannot move %s: %s would be at depth %d but subagent.maxDepth is %d", root.Address(), meta.Address(), ch.depth, max)
		}
		if meta.Closing.Active() {
			return errors.New("descendant has a dormant close reservation")
		}
		if meta.Consolidating != "" {
			return fmt.Errorf("cancel or finish consolidation before promoting a session with a reserved descendant")
		}
		meta.Depth = ch.depth
		if ch.id == id {
			if !meta.Closed.IsZero() {
				return ErrClosed
			}
			// Promotion changes hierarchy; it preserves a service session's routing and output contract.
			meta.ParentSession, meta.Owner = parent, parent
		}
		updates = append(updates, meta)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Once all locks are acquired, finish the short write consistently even
	// if the caller goes away. No further blocking lock waits occur here.
	for _, meta := range updates {
		if err := store.writeMeta(meta); err != nil {
			return err
		}
	}
	for i := len(releases) - 1; i >= 0; i-- {
		releases[i]()
	}
	releases = nil // Info may repair a turn and acquire its metadata lock.
	if root.ParentSession != parent || root.Depth != depth {
		event := "session_promoted"
		if parent != "" {
			event = "session_reparented"
		}
		store.appendEvent(id, map[string]any{"t": event, "previousParent": root.ParentSession, "previousDepth": root.Depth, "parent": parent, "depth": depth})
	}
	return nil
}
