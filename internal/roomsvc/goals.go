package roomsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/goalsvc"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/listpage"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func (s *Service) goalService() *goalsvc.Service {
	service := goalsvc.New()
	service.Notify = s.Notify
	root := s.sessions().Root
	if root == "" {
		root = subagent.DefaultStore().Root
	}
	service.Store = &goals.Store{Root: filepath.Join(filepath.Dir(root), "goals"), Issues: s.rooms()}
	return service
}
func (s *Service) Goals(roomRef, before string, limit int) (listpage.Page[goals.Goal], error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return listpage.Page[goals.Goal]{}, err
	}
	now := time.Now()
	window := goals.GoalPage{Count: listpage.Limit(limit)}
	if before == "" {
		window.ClosedAfter = now.Add(-listpage.Recent)
	} else {
		at, id, err := listpage.Boundary(before)
		if err != nil {
			return listpage.Page[goals.Goal]{}, err
		}
		window.BelowAt, window.BelowID = at, id
	}
	items, more, err := s.goalService().Store.Page(r.ID, window)
	if err != nil {
		return listpage.Page[goals.Goal]{}, err
	}
	return listpage.SelectWindow(items, before, limit, now, goalMeta, more)
}

// Goal reads one goal by ID, so a link opens a goal that no loaded page
// holds.
func (s *Service) Goal(roomRef, id string) (goals.Goal, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	return s.goalService().Store.Get(r.ID, id)
}

// GoalJournal returns saved journal entries, newest first. The cursor is
// the ID of the last returned entry; pass it as before for older entries.
func (s *Service) GoalJournal(roomRef, before string, limit int) (listpage.Page[goals.JournalEntry], error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return listpage.Page[goals.JournalEntry]{}, err
	}
	items, older, err := s.goalService().Store.Journal(r.ID, before, listpage.Limit(limit))
	if err != nil {
		return listpage.Page[goals.JournalEntry]{}, err
	}
	page := listpage.Page[goals.JournalEntry]{Items: items, Older: older}
	if older {
		page.Cursor = items[len(items)-1].ID
	}
	return page, nil
}

// GoalStatus returns one goal's mechanical status from live facts.
func (s *Service) GoalStatus(roomRef, id string) (goals.Status, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return goals.Status{}, err
	}
	return s.goalService().GoalStatus(r.ID, id)
}

// GoalEvents returns a goal's journal events, newest first. The cursor is
// the ID of the last returned event; pass it as before for older events.
func (s *Service) GoalEvents(roomRef, goal, before string, limit int) (listpage.Page[goals.Event], error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return listpage.Page[goals.Event]{}, err
	}
	items, older, err := s.goalService().Store.EventsPage(r.ID, goal, before, listpage.Limit(limit))
	if err != nil {
		return listpage.Page[goals.Event]{}, err
	}
	page := listpage.Page[goals.Event]{Items: items, Older: older}
	if older {
		page.Cursor = items[len(items)-1].ID
	}
	return page, nil
}

func goalPage(items []goals.Goal, before string, limit int) (listpage.Page[goals.Goal], error) {
	return listpage.Select(items, before, limit, time.Now(), goalMeta)
}

func goalMeta(g goals.Goal) listpage.Meta {
	at := g.UpdatedAt
	if g.Done != nil {
		at = g.Done.At
	}
	return listpage.Meta{ID: g.ID, At: at, Open: g.Open()}
}
func (s *Service) validGoalHolders(roomID string, in goals.Input) error {
	if in.Shared && len(in.Holders) > 0 {
		return fmt.Errorf("choose Room or named holders")
	}
	if !in.Shared && len(in.Holders) == 0 {
		return fmt.Errorf("choose Room or at least one holder")
	}
	for _, id := range in.Holders {
		m, err := s.sessions().Info(id)
		if err != nil || m.RoomID != roomID || m.Status == subagent.StatusClosed {
			return fmt.Errorf("goal holders must be open sessions in this room")
		}
	}
	return nil
}

// refuseRootHolders keeps the room's root seat out of goal holders. Root leads
// the room and gives the work goals to the agents that do the work.
func (s *Service) refuseRootHolders(holders []string) error {
	for _, id := range holders {
		if info, err := s.sessions().Info(id); err == nil && info.Role == subagent.RoleRoot {
			return errors.New(goals.RootHoldsNothing)
		}
	}
	return nil
}

func (s *Service) AddGoal(ctx context.Context, roomRef, text string, selectors []string, fixes []int) (goals.Goal, error) {
	return s.startGoal(ctx, roomRef, goals.Input{Text: text}, selectors, fixes)
}

// startGoal is the one way a goal starts: the dyna goal command, a room
// issue's assign action and every web entry point. It resolves the caller and
// the holder choice, refuses a root holder, then creates the goal, links the
// issues it fixes and posts its milestone. AddWithIssues deletes the new goal
// when a link fails, so a refused link leaves no goal behind.
func (s *Service) startGoal(ctx context.Context, roomRef string, in goals.Input, selectors []string, fixes []int) (goals.Goal, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	service := s.goalService()
	holders, shared, err := service.ResolveHolders(r.ID, author.ResolveAs, selectors)
	if err != nil {
		return goals.Goal{}, err
	}
	if err = s.refuseRootHolders(holders); err != nil {
		return goals.Goal{}, err
	}
	in.Holders, in.Shared = holders, shared
	actor := author.Address
	if author.ID == "owner" {
		actor = r.Name
	}
	g, err := service.Store.AddWithIssues(r.ID, author.ID, actor, in, fixes)
	if err != nil {
		return g, err
	}
	return g, service.Milestone(g, author.ID, "added")
}

func (s *Service) EditGoal(ctx context.Context, roomRef, id, text string, holders []string, shared bool) (goals.Goal, error) {
	return s.editGoal(ctx, roomRef, id, text, &holders, shared)
}

// EditGoalSelectors accepts CLI selectors; EditGoal accepts holder IDs from the web.
// Both use editGoal for validation, the store mutation and its milestone.
func (s *Service) EditGoalSelectors(ctx context.Context, roomRef, id, text string, selectors *[]string, fixes []int) (goals.Goal, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	var holders *[]string
	shared := false
	if selectors != nil {
		h, sh, err := s.goalService().ResolveHolders(r.ID, author.ResolveAs, *selectors)
		if err != nil {
			return goals.Goal{}, err
		}
		holders, shared = &h, sh
	}
	if len(fixes) > 0 && text == "" && selectors == nil {
		current, err := s.goalService().Store.Get(r.ID, id)
		if err != nil {
			return goals.Goal{}, err
		}
		text = current.Text
	}
	actor := author.Address
	if author.ID == "owner" {
		actor = r.Name
	}
	links, err := s.goalService().Store.PrepareIssueLinks(r.ID, actor, fixes)
	if err != nil {
		return goals.Goal{}, err
	}
	g, err := s.editGoal(ctx, r.ID, id, text, holders, shared)
	if err != nil || len(fixes) == 0 {
		return g, err
	}
	return g, s.goalService().Store.LinkPreparedIssues(r.ID, g.ID, actor, links)
}

func (s *Service) editGoal(ctx context.Context, roomRef, id, text string, holders *[]string, shared bool) (goals.Goal, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	if holders != nil {
		if err = s.validGoalHolders(r.ID, goals.Input{Holders: *holders, Shared: shared}); err != nil {
			return goals.Goal{}, err
		}
		if err = s.refuseRootHolders(*holders); err != nil {
			return goals.Goal{}, err
		}
	}
	service := s.goalService()
	before, err := service.Store.Get(r.ID, id)
	if err != nil {
		return goals.Goal{}, err
	}
	g, err := service.Store.Edit(r.ID, id, author.ID, text, holders, shared)
	if err != nil {
		return g, err
	}
	if before.Text != g.Text {
		err = service.Milestone(g, author.ID, "edited")
	}
	return g, err
}
func (s *Service) FinishGoal(ctx context.Context, roomRef, id, summary string, drop bool) (goals.Goal, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	service := s.goalService()
	g, err := service.Store.Finish(r.ID, id, author.ID, summary, drop)
	if err != nil {
		return g, err
	}
	verb := "finished"
	if drop {
		verb = "dropped"
	}
	return g, service.Milestone(g, author.ID, verb)
}

// ExpediteGoal makes goal id the room's one expedited goal, for goals.ExpediteTTL.
// An empty id stops the room's priority and returns the goal that had it.
// Only the owner or root may call it. Another expedited goal needs replace.
func (s *Service) ExpediteGoal(ctx context.Context, roomRef, id string, replace bool) (goals.Goal, error) {
	r, _, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	if err := authority.Check("goal expedite", authority.Of(ctx), nil); err != nil {
		return goals.Goal{}, err
	}
	store := s.goalService().Store
	if id == "" {
		return store.StopExpedite(r.ID)
	}
	return store.Expedite(r.ID, id, replace)
}

// AmendGoalBrief appends a correction to a goal's brief and sends it to every
// holder as the typed amendment post. Only the owner or root may call it.
func (s *Service) AmendGoalBrief(ctx context.Context, roomRef, id, text string) (goals.Goal, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Goal{}, err
	}
	if err := authority.Check("goal amend", authority.Of(ctx), nil); err != nil {
		return goals.Goal{}, err
	}
	g, err := s.goalService().Store.Amend(r.ID, id, author.ID, text)
	if err != nil {
		return g, err
	}
	// The holder reads the correction itself, so the post goes to the holders.
	// A shared goal has no holder session, so its post belongs to the room.
	to, bound := s.holderAddresses(g, r.Name)
	ev := room.GoalEvent{Type: room.GoalAmended, GoalID: g.ID, Title: g.Text, Actor: author.Name, State: room.GoalState(g.State), Summary: g.Amendments[len(g.Amendments)-1].Text, Amendment: len(g.Amendments)}
	_, err = s.Notify.Rooms.PostEventInput(room.PostEventInput{RoomID: r.ID, Event: ev, To: to, SessionIDs: bound})
	return g, err
}

// holderAddresses resolves a goal's holders to post addresses bound to the
// exact sessions. A goal the whole room holds, or one whose holders are gone,
// is addressed to the room itself.
func (s *Service) holderAddresses(g goals.Goal, roomName string) ([]string, map[string]string) {
	var to []string
	bound := map[string]string{}
	if !g.Shared {
		for _, holder := range g.Holders {
			info, err := s.sessions().Info(holder)
			if err != nil {
				continue
			}
			to = append(to, info.Address())
			bound[info.Address()] = info.ID
		}
	}
	if len(to) == 0 {
		return []string{roomName}, bound
	}
	return to, bound
}

// resolveAssignee turns one selector into one session id. An empty selector
// or "none" means no assignee; a group selector is not one session.
func (s *Service) resolveAssignee(roomID, by, sel string) (string, error) {
	if sel == "" || sel == "none" {
		return "", nil
	}
	holders, shared, err := s.goalService().ResolveHolders(roomID, by, []string{sel})
	if err != nil {
		return "", err
	}
	if shared || len(holders) != 1 {
		return "", fmt.Errorf("an item assignee is one session, not a group")
	}
	return holders[0], nil
}

// GoalItems lists a goal's items in suffix order.
func (s *Service) GoalItems(roomRef, goal string) ([]goals.Item, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	return s.goalService().Store.Items(r.ID, goal)
}

// GoalItemCounts reports each goal's open and total item counts.
func (s *Service) GoalItemCounts(roomRef string) (map[string][2]int, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	return s.goalService().Store.ItemCounts(r.ID)
}

func (s *Service) AddGoalItem(ctx context.Context, roomRef, goal, text, assigneeSel string) (goals.Item, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Item{}, err
	}
	assignee, err := s.resolveAssignee(r.ID, author.ResolveAs, assigneeSel)
	if err != nil {
		return goals.Item{}, err
	}
	return s.goalService().Store.AddItem(r.ID, goal, author.ID, text, assignee)
}

func (s *Service) EditGoalItem(ctx context.Context, roomRef, goal, item, text string, assigneeSel *string) (goals.Item, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Item{}, err
	}
	var assignee *string
	if assigneeSel != nil {
		id, err := s.resolveAssignee(r.ID, author.ResolveAs, *assigneeSel)
		if err != nil {
			return goals.Item{}, err
		}
		assignee = &id
	}
	return s.goalService().Store.EditItem(r.ID, goal, item, author.ID, text, assignee)
}

func (s *Service) MoveGoalItem(ctx context.Context, roomRef, goal, item, to string) (goals.ItemMove, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.ItemMove{}, err
	}
	// Promotion gives the item's assignee, or the caller, a goal of its own.
	if to == "new" {
		if err = s.refusePromotedRootHolder(r.ID, goal, item, author.ID); err != nil {
			return goals.ItemMove{}, err
		}
	}
	return s.goalService().Store.MoveItem(r.ID, goal, item, author.ID, to)
}

// refusePromotedRootHolder checks the session that would hold a promoted item.
func (s *Service) refusePromotedRootHolder(roomID, goal, item, by string) error {
	items, err := s.goalService().Store.Items(roomID, goal)
	if err != nil {
		return nil // MoveItem reports a missing goal or item
	}
	for _, it := range items {
		if it.ID != item {
			continue
		}
		holder := it.Assignee
		if holder == "" {
			holder = by
		}
		return s.refuseRootHolders([]string{holder})
	}
	return nil
}

func (s *Service) SetGoalItemState(ctx context.Context, roomRef, goal, item, state string) (goals.Item, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Item{}, err
	}
	return s.goalService().Store.SetItemState(r.ID, goal, item, author.ID, state)
}

// NoteGoal appends one holder note to the room's goal journal.
func (s *Service) NoteGoal(ctx context.Context, roomRef, goal, text string) (goals.Event, error) {
	r, author, err := s.goalCaller(ctx, roomRef)
	if err != nil {
		return goals.Event{}, err
	}
	if err := goals.CheckText(text); err != nil {
		return goals.Event{}, err
	}
	if _, err := s.goalService().Store.Get(r.ID, goal); err != nil {
		return goals.Event{}, err
	}
	return s.goalService().Store.AddEvent(r.ID, goals.Event{Goal: goal, By: author.ID, Event: "note", Text: strings.TrimSpace(text)})
}
func (s *Service) scanGoals(signatures, work map[string]string, version *string, first bool, emit func(Event)) {
	s.scanGoalsAt(time.Now(), signatures, work, version, first, emit)
}

func (s *Service) scanGoalsAt(now time.Time, signatures, work map[string]string, version *string, first bool, emit func(Event)) {
	service := s.goalService()
	service.Store.Now = func() time.Time { return now }
	current, err := service.Store.Revision()
	// Expiry changes the published goal even without a database write.
	const expiryKey = "\x00expediteUntil"
	const expiryPrefix = expiryKey + "\x00"
	const revisionPrefix = "\x00roomRevision\x00"
	expiry, _ := time.Parse(time.RFC3339Nano, work[expiryKey])
	if err != nil || current == *version && (expiry.IsZero() || now.Before(expiry)) {
		return
	}
	revisions, err := service.Store.RoomRevisions()
	if err != nil {
		return
	}
	all := first || *version == "" || work[revisionPrefix+goals.Global] != revisions[goals.Global]
	selected := map[string]bool{}
	for roomID, revision := range revisions {
		if roomID != goals.Global && (all || work[revisionPrefix+roomID] != revision) {
			selected[roomID] = true
		}
	}
	for key, value := range work {
		if strings.HasPrefix(key, revisionPrefix) {
			roomID := strings.TrimPrefix(key, revisionPrefix)
			if roomID != goals.Global {
				if _, exists := revisions[roomID]; all || !exists {
					selected[roomID] = true
				}
			}
		}
		if strings.HasPrefix(key, expiryPrefix) {
			at, err := time.Parse(time.RFC3339Nano, value)
			if err == nil && !now.Before(at) {
				selected[strings.TrimPrefix(key, expiryPrefix)] = true
			}
		}
	}
	var state goals.State
	if all {
		state, err = service.Store.Read()
	} else {
		rooms := make([]string, 0, len(selected))
		for roomID := range selected {
			rooms = append(rooms, roomID)
		}
		state, err = service.Store.ReadRooms(rooms)
	}
	if err != nil {
		return
	}
	// Prepare every input before advancing any caller-owned snapshot.
	nextWork := maps.Clone(work)
	for roomID := range selected {
		delete(nextWork, roomID)
		delete(nextWork, "rules\x00"+roomID)
		delete(nextWork, expiryPrefix+roomID)
		delete(nextWork, revisionPrefix+roomID)
	}
	rows := []listchange.Item{}
	present := map[string]bool{}
	wake, metadata := map[string]bool{}, map[string]bool{}
	for roomID, room := range state.Rooms {
		selected[roomID] = true
		items := make([]goals.Goal, 0, len(room.Goals))
		inputs := map[string]goals.Goal{}
		var roomExpiry time.Time
		for id, goal := range room.Goals {
			if goal.ExpeditedAt(now) && (roomExpiry.IsZero() || goal.ExpediteUntil.Before(roomExpiry)) {
				roomExpiry = *goal.ExpediteUntil
			}
			items = append(items, *goal)
			inputs[id] = *goal
			present[listchange.Key("goals", roomID, id)] = true
		}
		page, err := goalPage(items, "", 0)
		if err != nil {
			return
		}
		id := func(g goals.Goal) string { return g.ID }
		rows = append(rows, listchange.Rows("goals", roomID, items, listchange.HeadIDs(page.Items, id), id)...)
		data, err := json.Marshal(inputs)
		if err != nil {
			return
		}
		wake[roomID] = work[roomID] != string(data)
		nextWork[roomID] = string(data)
		rules, err := json.Marshal(struct {
			Global *goals.RuleSet
			Room   goals.RuleSet
		}{state.Global, room.RuleSet})
		if err != nil {
			return
		}
		key := "rules\x00" + roomID
		metadata[roomID] = work[key] != string(rules)
		nextWork[key] = string(rules)
		if !roomExpiry.IsZero() {
			nextWork[expiryPrefix+roomID] = roomExpiry.Format(time.RFC3339Nano)
		}
	}
	var gone []string
	for key := range signatures {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) == 3 && selected[parts[1]] && !present[key] {
			gone = append(gone, key)
			wake[parts[1]] = true
		}
	}
	var changes []listchange.Event
	if first {
		changes, err = listchange.Sync(signatures, rows, true)
	} else {
		changes, err = listchange.Patch(signatures, rows, gone)
	}
	if err != nil {
		return
	}
	delete(nextWork, revisionPrefix+goals.Global)
	for roomID, revision := range revisions {
		nextWork[revisionPrefix+roomID] = revision
	}
	delete(nextWork, expiryKey)
	var nextExpiry time.Time
	for key, value := range nextWork {
		if strings.HasPrefix(key, expiryPrefix) {
			at, _ := time.Parse(time.RFC3339Nano, value)
			if !at.IsZero() && (nextExpiry.IsZero() || at.Before(nextExpiry)) {
				nextExpiry = at
			}
		}
	}
	if !nextExpiry.IsZero() {
		nextWork[expiryKey] = nextExpiry.Format(time.RFC3339Nano)
	}
	clear(work)
	maps.Copy(work, nextWork)
	*version = current
	emitted := map[string]bool{}
	for _, change := range changes {
		value := change.Change
		emit(Event{Kind: "goals", RoomID: change.RoomID, Change: &value, WorkerWake: wake[change.RoomID]})
		wake[change.RoomID] = false
		emitted[change.RoomID] = true
	}
	if !first {
		for roomID, changed := range metadata {
			if changed && !emitted[roomID] {
				emit(Event{Kind: "goals", RoomID: roomID, WorkerWake: wake[roomID]})
			}
		}
	}
}
