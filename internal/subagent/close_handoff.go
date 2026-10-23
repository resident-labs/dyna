package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"

	"dyna-agent/internal/runstore"
)

// closeHeir follows stored parents, skipping closed ancestors, then uses the
// room's root session. Closing the root itself has no automatic successor.
func (m *Manager) closeHeir(info Info) (Info, error) {
	seen := map[string]bool{info.ID: true}
	for id := info.ParentSession; id != "" && !seen[id]; {
		seen[id] = true
		parent, err := m.store().ResolveSnapshot(id)
		if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			break
		}
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

// refuseGoalsForTopHeir refuses a close whose heir is root, or the owner when
// no ancestor is open. Root and the owner hold no goals, so such a goal would
// have nobody to keep working it. The message names each open goal with the
// commands that resolve it, and tells root to ask the owner when only the
// owner can unblock the work. A forced close refuses for the same reason.
func (m *Manager) refuseGoalsForTopHeir(info Info) error {
	held, err := m.goalOwned(map[string]Info{info.ID: info})
	if err != nil || len(held) == 0 {
		return nil
	}
	heir, err := m.closeHeir(info)
	if err != nil {
		return err
	}
	if heir.ID != "" && heir.Role != RoleRoot {
		return nil
	}
	goals := make([]Responsibility, len(held))
	for i, o := range held {
		goals[i] = o.r
	}
	return fmt.Errorf("%w\nRoot and the owner hold no goals, so a forced close keeps the work here instead of handing it to them. If only the owner can unblock a goal, ask the owner first with dyna ask. Then run one command above for each goal, or hand it to an open session.", responsibilityError("close", goals))
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
			if m.checkpoint != nil {
				m.checkpoint("close-before-goal-handoff")
			}
			// A goal moves only to a live agent. refuseGoalsForTopHeir already
			// refused a close that would leave the goal with root or the owner.
			moved, err := m.goalStore().HandoffHolder(info.RoomID, item.ID, info.ID, heir.ID)
			if err != nil {
				errs = append(errs, err)
			} else if moved {
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
