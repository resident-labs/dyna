package subagent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/instructions"
	"dyna-agent/internal/library"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/shells"
)

// sessionInstructions builds a session's standing instructions from its live
// state: the session rules, its static worker context, global and room rules,
// its service contract, its consolidation charter, and its spawn goal and
// brief. The runner freezes the result per harness conversation.
func (m *Manager) sessionInstructions(meta Meta, creator string, maxDepth int, charter string) instructions.Snapshot {
	goal, _ := m.spawnGoalBlock(meta)
	parts := []instructions.Section{
		{Name: "Session rules", Text: rulesFor(meta)},
		{Name: "Worker context", Text: workerContext(meta, creator, maxDepth)},
		m.profileInstructions(meta),
	}
	parts = append(parts, m.goalStore().InstructionSections(meta.RoomID)...)
	parts = append(parts, m.featureIndex(meta), m.runningJobs(meta))
	return instructions.Build(append(parts,
		m.openQuestions(meta),
		instructions.Section{Name: "Service contract", Text: meta.ServiceContract().Prompt()},
		instructions.Section{Name: "Consolidation charter", Text: charter},
		// Last, so the brief ends the instructions as it ended the old prompt.
		instructions.Section{Name: "Goal and brief", Text: goal},
	)...)
}

// featureIndex lists the room's repository features from its library map,
// one line per top-level feature with its summary and the ids of its
// children, nested ones flattened. Full entries stay out of the prompt;
// agents look them up with dyna library. Each line is an item, so a map
// change reaches a running session as a drift note naming the changed lines.
func (m *Manager) featureIndex(meta Meta) instructions.Section {
	section := instructions.Section{Name: "Feature index"}
	rec, err := m.rooms().Resolve(meta.RoomID)
	if err != nil || rec.Repo == "" {
		return section
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo, err := repoid.Resolve(ctx, rec.Repo)
	if err != nil {
		return section
	}
	entries, err := library.Store{DataDir: filepath.Dir(m.store().root())}.List(repo.ID, false)
	if err != nil {
		return section
	}
	byID := make(map[string]library.Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	children := make(map[string][]string)
	for _, e := range entries {
		if e.Parent == "" {
			continue
		}
		root := e.Parent
		for byID[root].Parent != "" {
			root = byID[root].Parent
		}
		children[root] = append(children[root], e.ID)
	}
	for _, e := range entries {
		if e.Parent != "" {
			continue
		}
		item := "- " + e.ID + ": " + strings.Join(strings.Fields(e.Summary), " ")
		if ids := children[e.ID]; len(ids) > 0 {
			slices.Sort(ids)
			item += " Children: " + strings.Join(ids, ", ") + "."
		}
		section.Items = append(section.Items, item)
	}
	if len(section.Items) > 0 {
		section.Text = "[DYNA FEATURES]\nTop-level features of this repository, from its library map. Look one up with `dyna library show <id>`; see every feature with `dyna library list --tree`."
	}
	return section
}

// runningJobs lists the background shell jobs this session started that are
// still running, one item each, so after a compaction the session still
// knows them. Starting, finishing or stopping one reaches the session as a
// drift note instead of a new prompt.
func (m *Manager) runningJobs(meta Meta) instructions.Section {
	section := instructions.Section{Name: "Your running jobs"}
	jobs, err := (&shells.Store{Root: filepath.Join(filepath.Dir(m.store().root()), "shells")}).List(false)
	if err != nil {
		return section
	}
	for _, j := range jobs {
		if j.Owner != meta.ID {
			continue
		}
		section.Items = append(section.Items, fmt.Sprintf("- %s · %s · started %s", j.ID, strings.Join(strings.Fields(j.Name), " "), j.Started.UTC().Format("2006-01-02 15:04 UTC")))
	}
	// List returns newest first; oldest first reads as a log and stays put.
	slices.Reverse(section.Items)
	if len(section.Items) > 0 {
		section.Text = "[DYNA RUNNING JOBS]\nBackground shell jobs you started that are still running. Read output with `dyna shell tail <id>`; stop one with `dyna shell stop <id>`."
	}
	return section
}

// profileInstructions lists the profiles this session's helpers may use. A
// service's helpers are services; everyone else starts subagent sessions.
func (m *Manager) profileInstructions(meta Meta) instructions.Section {
	section := instructions.Section{Name: "Subagent profiles"}
	profiles, err := m.profiles()
	if err != nil {
		section.Text = "[DYNA PROFILES]\nProfiles could not be read. Run dyna profiles list before delegation."
		return section
	}
	section.Text = "[DYNA PROFILES]\nEnabled subagent profiles. Ratings are out of 10; higher is better. Max-concurrent 0 means unlimited.\n" +
		"taste = choosing the solution.\n" +
		"diagnostics = finding what is wrong with a solution (reviews, debugging), with few false positives.\n" +
		"intelligence = executing a given solution on hard multi-step work.\n" +
		"cost = higher is cheaper."
	usable := (*profile.Profile).ForSubagents
	if meta.Service && meta.Role != RoleRoot {
		usable = (*profile.Profile).ForServices
	}
	for _, p := range profiles.EnabledProfiles() {
		if !usable(&p) {
			continue
		}
		model := strings.Join(strings.Fields(p.Model), " ")
		if model == "" {
			model = "-"
		}
		section.Items = append(section.Items, fmt.Sprintf("- %s | model: %s | taste: %d/10 | diagnostics: %d/10 | intelligence: %d/10 | cost-efficiency: %d/10 | max-concurrent: %d | %s", p.Name, model, p.Taste, p.EffectiveDiagnostics(), p.Intelligence, p.Cost, p.MaxConcurrent, strings.Join(strings.Fields(p.Description), " ")))
	}
	if len(section.Items) == 0 {
		section.Items = []string{"No enabled subagent profiles."}
	}
	return section
}

// workerContext is the part of the worker context that does not change
// during a conversation. Live selectors and goals stay in the turn prompt.
func workerContext(meta Meta, creator string, maxDepth int) string {
	limit := fmt.Sprintf("You are at depth %d.", meta.Depth)
	switch {
	case maxDepth > 0 && meta.Depth >= maxDepth:
		limit = fmt.Sprintf("You are at depth %d of %d, so start no subagent sessions. Workflows remain open.", meta.Depth, maxDepth)
	case maxDepth > 0:
		limit = fmt.Sprintf("You are at depth %d of %d.", meta.Depth, maxDepth)
	}
	if creator == "" {
		creator = meta.Room
	}
	if meta.Role == RoleRoot {
		return strings.Join([]string{
			"[DYNA WORKER IDENTITY]",
			fmt.Sprintf("You are the internal root of room %s, a long-lived Dyna session (%s).", meta.Room, meta.ID),
			"Room members: `dyna room members " + meta.Room + "`",
			"[/DYNA WORKER IDENTITY]",
		}, "\n")
	}
	return strings.Join([]string{
		"[DYNA WORKER IDENTITY]",
		fmt.Sprintf("You are %s, a long-lived Dyna subagent session (%s).", meta.Address(), meta.ID),
		"From: " + creator,
		"Room: " + meta.Room + "   You: " + meta.Address(),
		limit,
		"Room members: `dyna room members " + meta.Room + "`",
		"[/DYNA WORKER IDENTITY]",
	}, "\n")
}

// desiredInstructions is sessionInstructions for meta's current state.
func (m *Manager) desiredInstructions(meta Meta) instructions.Snapshot {
	creator := meta.Room
	if meta.ParentSession != "" {
		if parent, err := m.store().Inspect(meta.ParentSession); err == nil {
			creator = parent.Address()
		}
	}
	charter := ""
	if meta.Consolidation != nil && meta.Consolidation.Charter != "" {
		charter = m.consolidationCharter(*meta.Consolidation)
	}
	if manifest := m.consolidationManifest(meta); manifest != "" {
		charter = strings.TrimSpace(charter + "\n" + manifest)
	}
	return m.sessionInstructions(meta, creator, m.settings().SubagentMaxDepth, charter)
}

const instructionQuietPeriod = 3 * time.Second

// noteInstructions tells a session that its live instructions differ from
// its frozen snapshot. The note is a post from dyna, so it reaches the
// session like any post: steered into a running turn or
// drained by the next turn. It never wakes an idle session, and each note
// reports only what changed since the last one.
func (m *Manager) noteInstructions(ctx context.Context, id string) error {
	return m.noteInstructionsAt(ctx, id, time.Now())
}

func (m *Manager) noteInstructionsAt(ctx context.Context, id string, now time.Time) error {
	info, err := m.store().Info(id)
	if err != nil || info.Instructions == nil || info.Status == StatusClosed {
		return err
	}
	// Compare and post under the session lock a refresh also takes, so a
	// note and a refresh never cross. Compare with what the session was last
	// told, so a rule changed back or changed again gets its own note.
	var postErr error
	_, err = m.store().updateMetaContext(ctx, id, func(mm *Meta) {
		postErr = m.noteInstructionsLocked(mm, now)
	})
	return errors.Join(err, postErr)
}

// noteInstructionsLocked compares and posts while the caller holds the
// session lock. A room post remains outside the metadata transaction.
func (m *Manager) noteInstructionsLocked(meta *Meta, now time.Time) error {
	if !meta.Closed.IsZero() || !meta.InstructionsChangedAt.IsZero() && now.Before(meta.InstructionsChangedAt.Add(instructionQuietPeriod)) {
		return nil
	}
	known := meta.Instructions
	if meta.InstructionsKnown != nil {
		known = meta.InstructionsKnown
	}
	if known == nil {
		return nil
	}
	desired := m.desiredInstructions(*meta)
	changes := instructions.Changes(*known, desired)
	if len(changes) == 0 {
		meta.InstructionsChangedAt = time.Time{}
		return nil
	}
	// The session's own counter is the change's occurrence: a change that
	// reverts to an earlier state is a new note, a retry of one change keeps
	// its post, and a snapshot refresh does not reset it.
	event := room.SessionEvent{
		Type:       room.SessionInstructions,
		Session:    meta.Address(),
		Sections:   instructionChanges(changes),
		Detail:     instructions.Lines(changes),
		Occurrence: meta.InstructionsSeq,
	}
	msg, err := m.rooms().PostEventInput(room.PostEventInput{
		RoomID: meta.RoomID, Event: event, To: []string{meta.Address()},
		SessionIDs: map[string]string{meta.Address(): meta.ID},
	})
	if err != nil {
		return err
	}
	meta.InstructionsKnown = &desired
	// The counter moves only with a stored note, so a retry of one change
	// posts the same ID and the store keeps its first post.
	meta.InstructionsSeq++
	if msg.ID != "" {
		meta.InstructionsNotes = append(meta.InstructionsNotes, msg.ID)
	}
	meta.InstructionsChangedAt = time.Time{}
	return nil
}

// instructionChanges maps the change note's sections to the typed payload, so
// a card reads the same sections the agent text names.
func instructionChanges(changes []instructions.Change) []room.InstructionChange {
	actions := map[instructions.ChangeAction]room.InstructionAction{
		instructions.ChangeItems:   room.InstructionItems,
		instructions.ChangeBody:    room.InstructionBody,
		instructions.ChangeRemoved: room.InstructionRemoved,
	}
	out := make([]room.InstructionChange, len(changes))
	for i, change := range changes {
		out[i] = room.InstructionChange{Name: change.Name, Action: actions[change.Action], Added: change.Added, Removed: change.Removed, Body: change.Body}
	}
	return out
}

// NoteRoomInstructions queues a change for each open session in roomID,
// or every room for global edits. Persisting the time lets short CLI calls
// exit and lets a new engine finish a batch after a restart. It returns the
// session addresses the queued change can reach, so a rule's one owner row can
// carry the same reach as the per-session notes.
func (m *Manager) NoteRoomInstructions(ctx context.Context, roomID string) []string {
	if m == nil {
		return nil
	}
	if m.InstructionsChanged != nil {
		defer m.InstructionsChanged()
	}
	sessions, err := m.store().listMeta(false, true)
	if err != nil {
		return nil
	}
	var reached []string
	for _, meta := range sessions {
		if !meta.Closed.IsZero() || (roomID != "" && meta.RoomID != roomID) || meta.Instructions == nil {
			continue
		}
		queued := false
		_, err := m.store().updateMetaContext(ctx, meta.ID, func(mm *Meta) {
			if mm.Closed.IsZero() {
				mm.InstructionsChangedAt = time.Now()
				queued = true
			}
		})
		if err == nil && queued {
			reached = append(reached, meta.Address())
		}
	}
	slices.Sort(reached)
	return reached
}

// FlushInstructionNotes sends one combined note after three quiet seconds.
// The deadline is checked again under the session lock before posting. Shared
// changes also get one quiet owner summary; the agent's own notes stay in DMs.
// It returns when the next session's quiet period ends, or zero when none waits,
// so the engine can wake it then.
func (m *Manager) FlushInstructionNotes(ctx context.Context, now time.Time) (time.Time, error) {
	sessions, err := m.store().listMeta(false, true)
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	var failures []error
	var posted []room.Message
	for _, meta := range sessions {
		if meta.InstructionsChangedAt.IsZero() {
			continue
		}
		if due := meta.InstructionsChangedAt.Add(instructionQuietPeriod); now.Before(due) {
			if next.IsZero() || due.Before(next) {
				next = due
			}
			continue
		}
		before := len(meta.InstructionsNotes)
		if err := m.noteInstructionsAt(ctx, meta.ID, now); err != nil {
			failures = append(failures, err)
			continue
		}
		after, err := m.store().Info(meta.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if len(after.InstructionsNotes) <= before {
			continue
		}
		for _, id := range after.InstructionsNotes[before:] {
			msg, err := m.rooms().Message(meta.RoomID, id)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			posted = append(posted, msg)
		}
	}
	if err := m.postInstructionBatches(posted); err != nil {
		failures = append(failures, err)
	}
	return next, errors.Join(failures...)
}

// postInstructionBatches stores one quiet owner row for each exact diff that
// reached at least two sessions in this flush. Rule-only diffs use the rule's
// own row and its Sessions field instead. A change with one recipient has only
// that session's DM note and no room-chat row.
func (m *Manager) postInstructionBatches(notes []room.Message) error {
	type batch struct {
		roomID   string
		sections []room.InstructionChange
		sessions map[string]bool
		notes    map[string]bool
	}
	groups := map[string]*batch{}
	var order []string
	for _, msg := range notes {
		ev, ok := msg.InstructionNote()
		if !ok || len(ev.Sections) == 0 || ruleInstructionSections(ev.Sections) {
			continue
		}
		// Detail is the exact instruction diff. Grouping by it keeps shared
		// sections together without merging a session's own goal or job change.
		key := msg.RoomID + "\x00" + ev.Detail
		one := groups[key]
		if one == nil {
			one = &batch{roomID: msg.RoomID, sections: ev.Sections, sessions: map[string]bool{}, notes: map[string]bool{}}
			groups[key] = one
			order = append(order, key)
		}
		one.sessions[ev.Session] = true
		one.notes[msg.ID] = true
	}
	var failures []error
	for _, key := range order {
		one := groups[key]
		if len(one.sessions) < 2 {
			continue
		}
		sessions := make([]string, 0, len(one.sessions))
		for address := range one.sessions {
			sessions = append(sessions, address)
		}
		slices.Sort(sessions)
		noteIDs := make([]string, 0, len(one.notes))
		for id := range one.notes {
			noteIDs = append(noteIDs, id)
		}
		slices.Sort(noteIDs)
		rec, err := m.rooms().Resolve(one.roomID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		sum := sha256.Sum256([]byte(strings.Join(noteIDs, "\x00")))
		_, err = m.rooms().PostEventInput(room.PostEventInput{
			RoomID: one.roomID,
			ID:     fmt.Sprintf("msg_%x", sum[:8]),
			Event:  room.InstructionBatchEvent{Sections: one.sections, Sessions: sessions, Notes: noteIDs},
			To:     []string{rec.Name},
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// ruleInstructionSections reports a diff that the rule's own typed row already
// summarizes. Mixed diffs still get a batch for their non-rule sections.
func ruleInstructionSections(sections []room.InstructionChange) bool {
	if len(sections) == 0 {
		return false
	}
	for _, section := range sections {
		if section.Name != "Room rules" && section.Name != "Global rules" {
			return false
		}
	}
	return true
}

// beforeInstructionsRefresh lets a test change rules just before a refresh.
var beforeInstructionsRefresh = func() {}
