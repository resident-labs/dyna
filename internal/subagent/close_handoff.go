package subagent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"dyna-agent/internal/runstore"
)

// closeHeir follows stored parents, skipping closed ancestors, then uses the
// room's root session. Closing the root itself has no automatic successor.
func (m *Manager) closeHeir(info Info) (Info, error) {
	seen := map[string]bool{info.ID: true}
	for id := info.ParentSession; id != "" && !seen[id]; {
		seen[id] = true
		parent, err := m.store().ResolveSnapshot(id)
		if err != nil {
			return Info{}, err
		}
		if parent.RoomID != info.RoomID {
			return Info{}, errors.New("parent belongs to another room")
		}
		if parent.Closed.IsZero() && !parent.Closing.Active() {
			return parent, nil
		}
		id = parent.ParentSession
	}
	root, ok, err := m.RootSession(info.RoomID)
	if err != nil {
		return Info{}, err
	}
	if ok && root.ID != info.ID && !root.Closing.Active() {
		return root, nil
	}
	return Info{}, nil
}

// handoffClose runs after the turn stops and new assignments are fenced off.
// Each move commits through its existing store, so a retry moves only work left.
func (m *Manager) handoffClose(ctx context.Context, info, heir Info) ([]string, error) {
	var summary []string
	var errs []error
	if m.ClosePRs != nil {
		moved, err := m.ClosePRs(ctx, info, heir)
		summary = append(summary, moved...)
		errs = append(errs, err)
	}
	items, err := m.Responsibilities(ctx, info)
	if err != nil {
		return summary, errors.Join(append(errs, err)...)
	}
	for _, item := range items {
		switch item.Kind {
		case "workflow":
			if err := runstore.Cancel(item.ID); err != nil {
				errs = append(errs, fmt.Errorf("cancel workflow %s: %w", item.ID, err))
			} else {
				summary = append(summary, "Workflow "+item.ID+" canceled")
			}
		case "goal":
			if heir.ID == "" {
				continue
			}
			g, err := m.goalStore().Get(info.RoomID, item.ID)
			if err == nil {
				holders := slices.Clone(g.Holders)
				for i := range holders {
					if holders[i] == info.ID {
						holders[i] = heir.ID
					}
				}
				_, err = m.goalStore().Edit(info.RoomID, g.ID, heir.ID, "", &holders, false)
			}
			if err != nil {
				errs = append(errs, err)
			} else {
				summary = append(summary, "Goal "+item.ID+" → "+heir.Name)
			}
		case "item":
			summary = append(summary, "Goal item "+item.ID+" returned to its goal")
		case "question":
			summary = append(summary, "Question "+item.ID+" withdrawn")
		case "shell":
			summary = append(summary, "Shell "+item.ID+" stops unless kept")
		}
	}
	return summary, errors.Join(errs...)
}
