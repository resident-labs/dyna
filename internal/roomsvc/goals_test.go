package roomsvc

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

func TestGoalsUseRoomAuthorityAndQuietMilestones(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	other := e.room("other")
	lead := e.start(r, "lead", nil)
	stranger := e.start(other, "stranger", nil)
	// Root and agents add goals with the CLI, which writes the goal store.
	g, err := e.svc.goalService().Store.Add(r.ID, "owner", goals.Input{Text: "Ship the relay", Holders: []string{lead.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.EditGoal(t.Context(), r.ID, g.ID, "", []string{stranger.ID}, false); err == nil {
		t.Fatal("cross-room holder accepted")
	}
	if g.Setter != "owner" || !g.HeldBy(lead.ID) {
		t.Fatal(g)
	}

	g, err = e.svc.EditGoal(t.Context(), r.ID, g.ID, "Keep main green", nil, true)
	if err != nil || !g.Shared {
		t.Fatal(g, err)
	}
	g, err = e.svc.FinishGoal(t.Context(), r.ID, g.ID, "Passed", false)
	if err != nil || g.State != "done" {
		t.Fatal(g, err)
	}
	g, err = e.svc.EditGoal(t.Context(), r.ID, g.ID, "", []string{lead.ID}, false)
	if err != nil || g.State != "active" {
		t.Fatal(g, err)
	}
	all, err := e.svc.Goals(r.ID, "", 0)
	if err != nil || len(all.Items) != 2 || all.Items[0].ID != lead.Goal { // the lead's spawn goal, then g
		t.Fatal(all, err)
	}
	messages, err := e.svc.rooms().Messages(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	milestones := 0
	for _, msg := range messages {
		if msg.Kind == "goal" {
			milestones++
			if len(msg.To) != 1 || msg.To[0] != r.Name {
				t.Fatal("milestone woke a session", msg)
			}
		}
	}
	// Edited and finished; the CLI that added the goal posts its own milestone.
	if milestones != 2 {
		t.Fatal("unexpected milestones", milestones)
	}
	info, err := e.mgr.Store.Info(lead.ID)
	if err != nil || info.Turns != 1 {
		t.Fatal("goal edit started a turn", info, err)
	}
}
func TestSessionCloseHandsGoalsBackAndGoalsEmitEvents(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	child := e.start(r, "child", &lead)
	store := e.svc.goalService().Store
	g, err := store.Add(r.ID, child.ID, goals.Input{Text: "Ship", Holders: []string{child.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	version := ""
	work := map[string]string{}
	emit := func(ev Event) { events = append(events, ev) }
	goalRows := map[string]string{}
	e.svc.scanGoals(goalRows, work, &version, true, emit)
	if _, err := e.mgr.Close(e.ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	g, err = store.Get(r.ID, g.ID)
	if err != nil || !g.Open() || !g.HeldBy(lead.ID) || g.HeldBy(child.ID) {
		t.Fatal(g, err)
	}
	e.svc.scanGoals(goalRows, work, &version, false, emit)
	e.svc.scanGoals(goalRows, work, &version, false, emit)
	expected := map[string]bool{g.ID: true, child.Goal: true}
	if len(events) != len(expected) {
		t.Fatalf("changed goals: %+v", events)
	}
	for _, event := range events {
		if event.Kind != "goals" || event.RoomID != r.ID || event.Change == nil || event.Change.Op != "upsert" || !expected[event.Change.ID] {
			t.Fatalf("goal item: %+v", event)
		}
		delete(expected, event.Change.ID)
		var item goals.Goal
		if err := json.Unmarshal(event.Change.Item, &item); err != nil {
			t.Fatal(err)
		}
		if item.ID == g.ID {
			if !item.Open() || !item.HeldBy(lead.ID) {
				t.Fatalf("own goal close item: %+v", item)
			}
		} else if !item.HeldBy(lead.ID) {
			t.Fatalf("delegated goal handback item: %+v", item)
		}
	}
	// The lead keeps its spawn goal and inherits the child's held goal.
	held, err := store.OpenHeldBy(r.ID, lead.ID)
	if err != nil || len(held) != 2 || held[0].ID != lead.Goal {
		t.Fatal(held, err)
	}
	items, err := store.Items(r.ID, lead.Goal)
	if err != nil || len(items) != 1 || items[0].Assignee != "" || items[0].State != goals.ItemOpen {
		t.Fatal(items, err)
	}
}

func TestGoalJournalPagesNewestFirst(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	store := e.svc.goalService().Store
	var ids []string
	for range 3 {
		entry, err := store.AddJournal(r.ID, goals.JournalEntry{Entries: []goals.JournalLine{{Agent: "work:lead", Status: "on_track", Summary: "Fine."}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, entry.ID)
	}
	page, err := e.svc.GoalJournal(r.Name, "", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != ids[2] || !page.Older || page.Cursor != ids[1] {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = e.svc.GoalJournal(r.Name, page.Cursor, 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != ids[0] || page.Older || page.Cursor != "" {
		t.Fatalf("%+v %v", page, err)
	}
}

// Goal reads one goal by ID, even one too old for the first goals page, so
// a link can open it.
func TestGoalReadsOneGoal(t *testing.T) {
	e := newEnv(t)
	r := e.room("alpha")
	g, err := e.svc.goalService().Store.Add(r.ID, "owner", goals.Input{Text: "Old goal"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Goal(r.ID, g.ID)
	if err != nil || got.ID != g.ID || got.Text != "Old goal" {
		t.Fatalf("Goal = %+v, %v", got, err)
	}
	if _, err := e.svc.Goal(r.ID, "g999"); err == nil {
		t.Fatal("an unknown goal did not fail")
	}
}

func TestGoalItemAssigneeNoneClears(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	worker := e.start(r, "worker", nil)
	g, err := e.svc.goalService().Store.Add(r.ID, "owner", goals.Input{Text: "Ship the relay", Holders: []string{lead.ID}})
	if err != nil {
		t.Fatal(err)
	}
	it, err := e.svc.AddGoalItem(t.Context(), r.ID, g.ID, "task", "@worker")
	if err != nil || it.Assignee != worker.ID {
		t.Fatal(it, err)
	}
	if _, err := e.svc.AddGoalItem(t.Context(), r.ID, g.ID, "group", "@room"); err == nil {
		t.Fatal("group assignee accepted")
	}
	// The CLI documents --for none as clearing the assignee.
	none := "none"
	it, err = e.svc.EditGoalItem(t.Context(), r.ID, g.ID, it.ID, "", &none)
	if err != nil || it.Assignee != "" {
		t.Fatal(it, err)
	}
}

func TestGoalIssueLinks(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	ctx := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: lead.ID, Room: r.ID})
	first, err := e.svc.rooms().FileIssue(r.ID, room.IssueInput{Title: "Use the issue title", Filer: r.Name})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.rooms().FileIssue(r.ID, room.IssueInput{Title: "Link another issue", Filer: r.Name})
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.svc.AddGoal(ctx, r.ID, "", nil, []int{first.Number})
	if err != nil || g.Text != first.Title {
		t.Fatalf("goal = %+v, %v", g, err)
	}
	if _, err := e.svc.EditGoalSelectors(ctx, r.ID, g.ID, "", nil, []int{second.Number}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.rooms().Issues(r.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	linked := map[string]room.Issue{}
	for _, issue := range rows {
		linked[issue.ID] = issue
	}
	for _, issue := range []room.Issue{first, second} {
		if got := linked[issue.ID]; got.State != "goal" || got.GoalID != g.ID {
			t.Fatalf("%s link = %+v, want %s", issue.Label(), got, g.ID)
		}
	}
}

func TestGoalStatusReadsLiveFacts(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	g, err := e.svc.goalService().Store.Add(r.ID, "owner", goals.Input{Text: "Ship", Holders: []string{lead.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Elapsed time alone must not mark an idle holder stuck.
	if err := e.svc.goalService().Store.Update(func(state *goals.State) error {
		state.Room(r.ID).Goals[g.ID].CreatedAt = time.Now().Add(-7 * time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.GoalStatus(r.ID, g.ID)
	if err != nil || st.Label != goals.StatusOnTrack {
		t.Fatalf("status = %+v err=%v", st, err)
	}
}

func TestGoalEventsPageNewestFirst(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	store := e.svc.goalService().Store
	g, err := store.Add(r.ID, "owner", goals.Input{Text: "Ship", Holders: []string{lead.ID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range []string{"first", "second", "third"} {
		if _, err := store.AddEvent(r.ID, goals.Event{Goal: g.ID, By: lead.ID, Event: "note", Text: note}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := e.svc.GoalEvents(r.ID, g.ID, "", 2)
	if err != nil || len(page.Items) != 2 || !page.Older {
		t.Fatalf("page = %+v err=%v", page, err)
	}
	if page.Items[0].Text != "third" || page.Items[1].Text != "second" {
		t.Fatalf("order = %+v", page.Items)
	}
	older, err := e.svc.GoalEvents(r.ID, g.ID, page.Cursor, 2)
	if err != nil || len(older.Items) != 1 || older.Older || older.Items[0].Text != "first" {
		t.Fatalf("older page = %+v err=%v", older, err)
	}
}

// A correction reaches the holder as its own post. Repeating an earlier
// correction is still a new post, because its stored position names it.
func TestAmendGoalBriefPostsEachAmendmentOnce(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	lead := e.start(r, "lead", nil)
	store := e.svc.goalService().Store
	g, err := store.Add(r.ID, "owner", goals.Input{Text: "Ship the relay", Detail: "Own internal/room only.", Holders: []string{lead.ID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Also update the guide.", "Skip the parser refactor.", "Also update the guide."} {
		if _, err := e.svc.AmendGoalBrief(t.Context(), r.ID, g.ID, text); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := e.svc.rooms().Messages(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	texts := []string{}
	for _, msg := range messages {
		if ev, ok := msg.Goal(); ok && ev.Type == room.GoalAmended {
			ids[msg.ID] = true
			texts = append(texts, ev.Summary)
			if len(msg.To) != 1 || msg.To[0] != lead.Address() {
				t.Fatalf("amendment post was not addressed to the holder: %+v", msg)
			}
		}
	}
	want := []string{"Also update the guide.", "Skip the parser refactor.", "Also update the guide."}
	if len(ids) != 3 || len(texts) != 3 || texts[0] != want[0] || texts[1] != want[1] || texts[2] != want[2] {
		t.Fatalf("amendment posts = %v (ids %v), want %v", texts, ids, want)
	}
	if stored, err := store.Get(r.ID, g.ID); err != nil || stored.Detail != "Own internal/room only." || len(stored.Amendments) != 3 || stored.Text != "Ship the relay" {
		t.Fatalf("stored goal = %+v, %v", stored, err)
	}
}
