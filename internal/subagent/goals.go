package subagent

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/goals"
)

func (m *Manager) goalStore() *goals.Store {
	return &goals.Store{Root: filepath.Join(filepath.Dir(m.store().root()), "goals"), Issues: m.rooms()}
}

// addSpawnGoal creates the work a new session holds: an existing goal, a
// delegation item on an effective goal, or a goal of its own when none is
// eligible. Service sessions hold no goals, and root holds no goals either.
func (m *Manager) addSpawnGoal(meta Meta, under, text, brief string, fixes []int) (goalID, itemID string, err error) {
	if meta.Service {
		if len(fixes) > 0 {
			return "", "", fmt.Errorf("--fixes needs a worker goal")
		}
		return "", "", nil
	}
	setter := meta.ParentSession
	if setter == "" {
		setter = "owner"
	}
	actor := meta.Room
	if meta.ParentSession != "" {
		if parent, err := m.store().ReadMeta(meta.ParentSession); err == nil {
			actor = parent.Address()
		}
	}
	store := m.goalStore()
	if goal, ok := goalReference(text); ok {
		if under != "" {
			return "", "", fmt.Errorf("--goal and --under cannot be used together when --goal names an existing goal")
		}
		g, err := store.Get(meta.RoomID, goal)
		if err != nil {
			return "", "", err
		}
		if !g.Open() {
			return "", "", fmt.Errorf("goal %s is not open", goal)
		}
		if m.rootHeld(meta.RoomID, goal) {
			return "", "", fmt.Errorf("goal %s is held by root, which holds no goals", goal)
		}
		links, err := store.PrepareIssueLinks(meta.RoomID, actor, fixes)
		if err != nil {
			return "", "", err
		}
		if err := store.LinkPreparedIssues(meta.RoomID, goal, actor, links, goals.ClaimAny); err != nil {
			return "", "", err
		}
		if !g.Shared && !g.HeldBy(meta.ID) {
			holders := append(append([]string{}, g.Holders...), meta.ID)
			if _, err := store.Edit(meta.RoomID, goal, setter, "", &holders, false); err != nil {
				return "", "", errors.Join(fmt.Errorf("cannot assign the session's goal: %w", err), store.RestoreIssueLinks(meta.RoomID, actor, links))
			}
		}
		return goal, "", nil
	}
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
			links, err := store.PrepareIssueLinks(meta.RoomID, actor, fixes)
			if err != nil {
				return "", "", err
			}
			if err := store.LinkPreparedIssues(meta.RoomID, goal, actor, links, goals.ClaimAny); err != nil {
				return "", "", err
			}
			itemText := text
			if brief != "" {
				itemText += "\n\nBrief:\n" + brief
			}
			item, err := store.AddItem(meta.RoomID, goal, setter, itemText, meta.ID)
			if err != nil {
				return "", "", errors.Join(fmt.Errorf("cannot create the session's item: %w", err), store.RestoreIssueLinks(meta.RoomID, actor, links))
			}
			return goal, item.ID, nil
		}
	}
	g, err := store.AddWithIssues(meta.RoomID, setter, actor, goals.Input{Text: text, Detail: brief, Holders: []string{meta.ID}}, fixes, goals.ClaimAny)
	if err != nil {
		return "", "", fmt.Errorf("cannot create the session's goal: %w", err)
	}
	return g.ID, "", nil
}

func goalReference(text string) (string, bool) {
	if len(text) < 2 || text[0] != 'g' {
		return "", false
	}
	for _, digit := range text[1:] {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	return text, true
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
			block += amendmentLines(g.Amendments)
			return block, brief != "" || len(g.Amendments) > 0
		}
		return "", false
	}
	block = fmt.Sprintf("[DYNA GOAL AND BRIEF]\nGoal %s: %s\nWhen it is done, run `dyna goal done %s \"<summary>\"`.", g.ID, g.Text, g.ID)
	brief := g.Detail
	if meta.SpawnBrief != "" {
		brief = meta.SpawnBrief
	}
	if brief != "" {
		block += "\n\nBrief:\n" + brief
	}
	block += amendmentLines(g.Amendments)
	return block, brief != "" || len(g.Amendments) > 0
}

// amendmentLines lists the brief amendments in stored order, so a session that
// joins late or refreshes after compaction reads every correction.
func amendmentLines(amendments []goals.Amendment) string {
	if len(amendments) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nBrief amendments (oldest first):")
	for _, a := range amendments {
		b.WriteString("\n- ")
		b.WriteString(a.Text)
	}
	return b.String()
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
