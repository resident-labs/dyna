package subagent

import (
	"context"
	"encoding/json"
	"errors"

	"dyna-agent/internal/sessionlife"

	"dyna-agent/internal/worktrees"
)

// RetainClosedSession is maintenance's per-pass check for long-closed
// sessions. It reads every record once to find the sessions others still
// read: a consolidation target reads its sources through InheritedInboxes,
// and delivery follows a source's ConsumedBy to its target. A source stays
// while a remaining record names it, so chains of consolidations stay whole.
func (m *Manager) RetainClosedSession(ctx context.Context) (func(context.Context, string) (bool, error), error) {
	referenced := map[string]bool{}
	exists := map[string]bool{}
	consumedBy := map[string]string{}
	err := sessionlife.Each(m.store().root(), false, func(id string, raw []byte, err error) error {
		exists[id] = true
		var meta Meta
		if err != nil || json.Unmarshal(raw, &meta) != nil {
			return nil // unreadable records are not removed (the retain check reads them again)
		}
		for _, in := range meta.InheritedInboxes {
			referenced[in.Session] = true
		}
		if meta.ConsumedBy != "" {
			consumedBy[id] = meta.ConsumedBy
		}
		return ctx.Err()
	})
	if err != nil && !errors.Is(err, sessionlife.ErrRecord) {
		return nil, err
	}
	return func(ctx context.Context, id string) (bool, error) {
		if referenced[id] || consumedBy[id] != "" && exists[consumedBy[id]] {
			return true, nil
		}
		return m.KeepClosedSession(ctx, id)
	}, nil
}

// KeepClosedSession reports whether a long-closed session still matters, so
// maintenance keeps its folder and record: it has open children, open goals
// it holds or set, other open work (PR roles, questions, shell jobs, runs),
// or a kept worktree.
func (m *Manager) KeepClosedSession(ctx context.Context, id string) (bool, error) {
	info, err := m.store().Info(id)
	if err != nil {
		return false, err
	}
	children, err := m.OpenChildren(id)
	if err != nil || len(children) > 0 {
		return true, err
	}
	if info.RoomID != "" {
		open, err := m.goalStore().ListOpen(info.RoomID)
		if err != nil {
			return true, err
		}
		for _, g := range open {
			if g.Setter == id || g.HeldBy(id) {
				return true, nil
			}
		}
	}
	work, err := m.Responsibilities(ctx, info)
	if err != nil || len(work) > 0 {
		return true, err
	}
	trees, err := worktrees.List()
	if err != nil {
		return true, err
	}
	for _, tree := range trees {
		if tree.Session == id {
			return true, nil
		}
	}
	return false, nil
}
