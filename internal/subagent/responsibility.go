package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
func (m *Manager) ResponsibilityCounts(ctx context.Context, infos []Info) (map[string]map[string]int, error) {
	items, err := m.ownedBy(ctx, infos, false)
	counts := make(map[string]map[string]int, len(items))
	for _, item := range items {
		byKind := counts[item.info.ID]
		if byKind == nil {
			byKind = map[string]int{}
			counts[item.info.ID] = byKind
		}
		byKind[string(item.r.Kind)]++
	}
	return counts, err
}

// ownedBy reads each store once and returns what infos own, with the error of
// any store that could not be read. A caller that decides whether a session may
// close must refuse on that error; a count may use what was read. Only the close
// path repairs a session whose turn runner is gone (repair), because a web read
// must not change a session or a room file.
func (m *Manager) ownedBy(ctx context.Context, infos []Info, repair bool) ([]owned, error) {
	byID := make(map[string]Info, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
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
		sessionlife.WithPublication(ctx, state.Root(base), "state.lock", collect(func() ([]owned, error) { return m.prOwned(byID) })),
		collect(func() ([]owned, error) { return m.goalOwned(byID) })(),
		collect(func() ([]owned, error) { return m.itemOwned(byID) })(),
		collect(func() ([]owned, error) { return m.questionOwned(byID) })(),
		sessionlife.WithPublication(ctx, filepath.Join(base, "shells"), "publication.lock", collect(func() ([]owned, error) { return m.shellOwned(byID) })),
		sessionlife.WithPublication(ctx, filepath.Join(base, "runs"), "publication.lock", collect(func() ([]owned, error) { return m.runOwned(byID) })),
		collect(func() ([]owned, error) { return m.childOwned(byID, repair) })(),
	)
	return out, err
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
// link that is not active owns the session nothing.
func (m *Manager) prOwned(byID map[string]Info) ([]owned, error) {
	root := state.Root(filepath.Dir(m.store().root()))
	var out []owned
	var errs []error
	for roomID, infos := range byRoom(byID) {
		owners := make(map[string]Info, len(infos))
		ids := make([]string, 0, len(infos))
		for _, info := range infos {
			owners[info.ID] = info
			ids = append(ids, info.ID)
		}
		st, err := state.ReadForSessions(root, roomID, ids...)
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
		for _, id := range uniqueIDs(job.ParentSession, job.Session) {
			info, ok := byID[id]
			if !ok {
				continue
			}
			out = append(out, owned{info: info, r: Responsibility{Kind: "workflow", ID: job.ID, Text: "owns live workflow run " + job.ID + "; cancel it: dyna runs cancel " + job.ID}})
		}
	}
	return out, nil
}

// uniqueIDs drops the empty and repeated IDs, so a run whose parent session is
// also its session counts once.
func uniqueIDs(ids ...string) []string {
	out := ids[:0]
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
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
