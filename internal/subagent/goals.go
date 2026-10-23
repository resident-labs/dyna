package subagent

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/goals"
)

func (m *Manager) goalStore() *goals.Store {
	return &goals.Store{Root: filepath.Join(filepath.Dir(m.store().root()), "goals")}
}

// addSpawnGoal creates the work a new session holds: a delegation item on
// an effective goal for a child, or a goal of its own when none is eligible.
// Service sessions hold no goals, and root holds no goals either, so an
// implicit lookup never picks a root goal. An explicit --under still selects
// its goal, so a bad ID stays an error.
func (m *Manager) addSpawnGoal(meta Meta, under, text, brief string) (goalID, itemID string, err error) {
	if meta.Service {
		return "", "", nil
	}
	setter := meta.ParentSession
	if setter == "" {
		setter = "owner"
	}
	store := m.goalStore()
	implicit := meta.ParentSession != "" && !m.isRootSession(meta.ParentSession)
	if implicit || under != "" {
		goal, err := store.EffectiveGoal(meta.RoomID, meta.ParentSession, under)
		if err != nil {
			return "", "", err
		}
		if goal != "" {
			if m.rootHeld(meta.RoomID, goal) {
				return "", "", fmt.Errorf("goal %s is held by root, which holds no goals; start without --under, or use a goal an agent holds", goal)
			}
			itemText := text
			if brief != "" {
				itemText += "\n\nBrief:\n" + brief
			}
			item, err := store.AddItem(meta.RoomID, goal, setter, itemText, meta.ID)
			if err != nil {
				return "", "", fmt.Errorf("cannot create the session's item: %w", err)
			}
			return goal, item.ID, nil
		}
	}
	g, err := store.Add(meta.RoomID, setter, goals.Input{Text: text, Detail: brief, Holders: []string{meta.ID}})
	if err != nil {
		return "", "", fmt.Errorf("cannot create the session's goal: %w", err)
	}
	return g.ID, "", nil
}

// isRootSession reports whether id is a room's internal root session.
func (m *Manager) isRootSession(id string) bool {
	if id == "" {
		return false
	}
	meta, err := m.store().ReadMeta(id)
	return err == nil && meta.Role == RoleRoot
}

// rootHeld reports whether root holds the goal, so no child joins it.
func (m *Manager) rootHeld(roomID, goalID string) bool {
	g, err := m.goalStore().Get(roomID, goalID)
	if err != nil {
		return false
	}
	for _, id := range g.Holders {
		if m.isRootSession(id) {
			return true
		}
	}
	return false
}

// spawnGoalBlock is the session's open spawn goal and its brief under one
// heading. The first prompt and the post-compaction resend both use it.
// hasBrief reports whether the block holds the brief.
func (m *Manager) spawnGoalBlock(meta Meta) (block string, hasBrief bool) {
	if meta.Goal == "" {
		return "", false
	}
	g, err := m.goalStore().Get(meta.RoomID, meta.Goal)
	if err != nil || !g.Open() {
		return "", false
	}
	if meta.Item != "" {
		items, err := m.goalStore().Items(meta.RoomID, meta.Goal)
		if err != nil {
			return "", false
		}
		for _, it := range items {
			if it.ID != meta.Item || it.State != goals.ItemOpen {
				continue
			}
			head, brief, _ := strings.Cut(it.Text, "\n\nBrief:\n")
			block = fmt.Sprintf("[DYNA GOAL AND BRIEF]\nGoal %s item %s: %s\nWhen it is done, run `dyna goal item done %s %s`.", g.ID, it.ID, head, g.ID, it.ID)
			if brief != "" {
				block += "\n\nBrief:\n" + brief
			}
			return block, brief != ""
		}
		return "", false
	}
	block = fmt.Sprintf("[DYNA GOAL AND BRIEF]\nGoal %s: %s\nWhen it is done, run `dyna goal done %s \"<summary>\"`.", g.ID, g.Text, g.ID)
	if g.Detail != "" {
		block += "\n\nBrief:\n" + g.Detail
	}
	return block, g.Detail != ""
}

// goalFrame leaves out the goal shown in full elsewhere in the same prompt.
func (m *Manager) goalFrame(meta Meta, shown string) string {
	all, err := m.goalStore().ListOpen(meta.RoomID)
	if err != nil {
		return ""
	}
	// Root and service sessions hold no goals, so nothing asks them to set one.
	if meta.Service {
		return ""
	}
	if shown != "" {
		all = slices.DeleteFunc(all, func(g goals.Goal) bool { return g.ID == shown })
		if !slices.ContainsFunc(all, func(g goals.Goal) bool { return g.Open() && g.Concerns(meta.ID) }) {
			return ""
		}
	}
	// An empty room still gets the frame: it nudges the session to set a goal.
	names := map[string]string{"owner": "owner"}
	if sessions, err := m.store().ListSnapshot(false); err == nil {
		for _, s := range sessions {
			if s.RoomID == meta.RoomID {
				names[s.ID] = s.Name
			}
		}
	}
	counts, _ := m.goalStore().ItemCounts(meta.RoomID)
	return goals.Frame(all, meta.ID, names, counts)
}

// closeGoals drops the closed session's own work and returns delegated work.
// The heir maps historical goals; consolidation transfers open work with Reassign.
func (m *Manager) closeGoals(info Info, heir string) {
	// Unfinished items return to their goal before any goal handover.
	_, _ = m.goalStore().ReturnItems(info.RoomID, info.ID)
	// No goals store means there is nothing to change.
	all, err := m.goalStore().List(info.RoomID)
	if err != nil || len(all) == 0 {
		return
	}
	open := map[string]bool{}
	if sessions, err := m.store().List(false); err == nil {
		for _, s := range sessions {
			// Root holds no goals, so a goal with no other live session left
			// is dropped with a note instead of waiting for an owner.
			if s.RoomID == info.RoomID && s.Role != RoleRoot {
				open[s.ID] = true
			}
		}
	}
	_ = m.goalStore().CloseSession(info.RoomID, info.ID, heir, open)
}
