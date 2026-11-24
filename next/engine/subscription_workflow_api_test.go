package engine

import (
	"database/sql"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSubscriptionFireUpdateAndStateKeepQueueAndState(t *testing.T) {
	w := setupWorkflow(t)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Key: "original"})
	key := "pr-1"
	state, problem := w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub, Key: &key, Value: json.RawMessage(`{"head":"old"}`)})
	if problem != nil || len(state.States) != 1 || string(state.States[0].Value) != `{"head":"old"}` {
		t.Fatalf("state %+v %v", state, problem)
	}
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub, Key: &key}); problem != nil {
		t.Fatal(problem)
	}
	listedSubscriptions, listProblem := w.SubscriptionList(w.ctx, w.owner, contract.SubscriptionListRequest{Room: w.room})
	if listProblem != nil {
		t.Fatal(listProblem)
	}
	found := false
	for _, info := range listedSubscriptions.Subscriptions {
		if info.Subscription.ID == sub {
			found = true
			if info.Waiting != 1 {
				t.Fatalf("waiting: %+v", info)
			}
		}
	}
	if !found {
		t.Fatal("missing subscription")
	}
	changedKey := "new-template"
	latest := true
	parent := w.root
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Workflow: &contract.WorkflowSubscription{Script: serviceScript, Args: map[string]json.RawMessage{"updated": json.RawMessage("true")}}, Key: &changedKey, Latest: &latest, Parent: &parent}); problem != nil {
		t.Fatal(problem)
	}
	stored, err := w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Key != changedKey || !stored.Latest || stored.Parent != w.root {
		t.Fatalf("update %+v", stored)
	}
	var queuedKey string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT key FROM workflow_queue WHERE sub=?", sub).Scan(&queuedKey); err != nil {
		t.Fatal(err)
	}
	if queuedKey != key {
		t.Fatal("update changed queued key")
	}
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	run, err := w.store.ServiceRun(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	events, problem := w.WorkflowEvents(w.ctx, sessionCaller(run.Session), contract.WorkflowEventsRequest{Boot: "boot", Limit: 10})
	if problem != nil || len(events.Events) != 1 || events.Events[0].Key != key {
		t.Fatalf("hand-over %+v %v", events, problem)
	}
	listedSubscriptions, listProblem = w.SubscriptionList(w.ctx, w.owner, contract.SubscriptionListRequest{Room: w.room})
	if listProblem != nil {
		t.Fatal(listProblem)
	}
	for _, info := range listedSubscriptions.Subscriptions {
		if info.Subscription.ID == sub && info.Waiting != 0 {
			t.Fatalf("event in a handler counted as waiting: %+v", info)
		}
	}
	got, problem := w.WorkflowState(w.ctx, sessionCaller(run.Session), contract.WorkflowStateRequest{Row: events.Events[0].Row})
	if problem != nil || string(got.Value) != `{"head":"old"}` {
		t.Fatalf("real state %s %v", got.Value, problem)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run.Args), &args); err != nil {
		t.Fatal(err)
	}
	if string(args["updated"]) != "true" {
		t.Fatal("the running service did not take the updated script args")
	}
	listed, problem := w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub})
	if problem != nil || len(listed.States) != 1 {
		t.Fatalf("list %+v %v", listed, problem)
	}
	reset, problem := w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub, Key: &key, Value: json.RawMessage("null")})
	if problem != nil || string(reset.States[0].Value) != "null" {
		t.Fatalf("reset %+v %v", reset, problem)
	}
	listed, problem = w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub})
	if problem != nil || len(listed.States) != 0 {
		t.Fatal("reset left a stored key")
	}
	var worker contract.SessionID
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM sessions WHERE name='worker'").Scan(&worker); err != nil {
		t.Fatal(err)
	}
	if _, problem := w.SubscriptionFire(w.ctx, sessionCaller(worker), contract.SubscriptionFireRequest{ID: sub}); problem == nil {
		t.Fatal("worker fired root subscription")
	}
	if _, problem := w.SubscriptionUpdate(w.ctx, sessionCaller(worker), contract.SubscriptionUpdateRequest{ID: sub}); problem == nil {
		t.Fatal("worker updated root subscription")
	}
	if _, problem := w.SubscriptionState(w.ctx, sessionCaller(worker), contract.SubscriptionStateRequest{ID: sub}); problem == nil {
		t.Fatal("worker read root state")
	}
}

func TestWorkflowRunLimitLeavesEventUnclaimed(t *testing.T) {
	w := setupWorkflow(t)
	one := 1
	w.set(w.room, contract.WorkflowSettings{MaxRuns: &one})
	busy, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: serviceScript})
	if problem != nil {
		t.Fatal(problem)
	}
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Latest: true})
	tick := func() {
		t.Helper()
		if err := w.fireDue(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub}); problem != nil {
		t.Fatal(problem)
	}
	for range 3 {
		tick()
	}
	var queued string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT run FROM workflow_queue WHERE sub=?", sub).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != "" {
		t.Fatal("capacity refusal claimed event")
	}
	stored, err := w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Failures != 0 || !stored.HoldUntil.IsZero() {
		t.Fatal("capacity refusal backed off")
	}
	if err := w.store.Write(w.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(w.ctx, "UPDATE workflow_runs SET state='done' WHERE id=?", busy.Run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tick()
	var n int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_runs WHERE sub=?", sub).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("waiting event did not start next tick")
	}
}

func TestSubscriptionUpdateFiltersAndManualFire(t *testing.T) {
	w := setupWorkflow(t)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.pr.changed"}, Where: []string{"kind=pushed"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Key: "{repo}#{number}"})
	filters := []string{"kind=opened,pushed"}
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Where: &filters}); problem != nil {
		t.Fatal(problem)
	}
	filters = []string{"kind="}
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Where: &filters}); problem == nil {
		t.Fatal("invalid filter accepted")
	}
	stored, err := w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Where) != 1 || stored.Where[0] != "kind=opened,pushed" {
		t.Fatal("invalid update changed filters")
	}
	event := json.RawMessage(`{"type":"github.pr.changed","body":{"changes":[{"pr":{"repo":"owner/repo","number":7,"head":"abc"},"kinds":["comment"]}]}}`)
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub, Event: event}); problem != nil {
		t.Fatal(problem)
	}
	var key string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT key FROM workflow_queue WHERE sub=?", sub).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "owner/repo#7" {
		t.Fatalf("resolved key %q", key)
	}
	literal := "{repo}"
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub, Key: &literal, Event: event}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub, Event: json.RawMessage(`{"type":"github.pr.changed","body":{"changes":[]}}`)}); problem == nil {
		t.Fatal("empty PR event accepted")
	}
	if _, problem := w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub, Value: json.RawMessage("null")}); problem == nil {
		t.Fatal("write without key accepted")
	}
	filters = []string{}
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Where: &filters}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Where) != 0 {
		t.Fatal("clear filters failed")
	}
}

func TestEnablingLatestCoalescesOnTheNextEvent(t *testing.T) {
	w := setupWorkflow(t)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}})
	for range 2 {
		if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub}); problem != nil {
			t.Fatal(problem)
		}
	}
	latest := true
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Latest: &latest}); problem != nil {
		t.Fatal(problem)
	}
	var count int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=?", sub).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("update discarded queued events")
	}
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub}); problem != nil {
		t.Fatal(problem)
	}
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=?", sub).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("new event did not coalesce waiting events")
	}
}

func TestLatestRealDispatchCoalescesWaitingEvents(t *testing.T) {
	w := setupWorkflow(t)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}})
	for range 2 {
		if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub}); problem != nil {
			t.Fatal(problem)
		}
	}
	latest := true
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Latest: &latest}); problem != nil {
		t.Fatal(problem)
	}
	recordNotice(t, w.Service, w.ctx, w.room)
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=? AND run=''", sub).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("latest retained %d waiting copies; want 1", count)
	}
	var queuedSeq, newestSeq int64
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT seq FROM workflow_queue WHERE sub=?", sub).Scan(&queuedSeq); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT max(seq) FROM events WHERE type='notice.posted'").Scan(&newestSeq); err != nil {
		t.Fatal(err)
	}
	if queuedSeq != newestSeq {
		t.Fatalf("queued sequence %d, newest %d", queuedSeq, newestSeq)
	}
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	run, err := w.store.ServiceRun(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	events, problem := w.WorkflowEvents(w.ctx, sessionCaller(run.Session), contract.WorkflowEventsRequest{Boot: "boot", Limit: 10})
	if problem != nil || len(events.Events) != 1 {
		t.Fatalf("hand-over %+v %v", events, problem)
	}
}

func TestSubscriptionUpdateOnKeepsStateQueueAndServiceAndDeliversAddedType(t *testing.T) {
	w := setupWorkflow(t)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{
		Scope:    contract.ScopeRoom,
		Room:     w.room,
		On:       []contract.EventType{"github.pr.changed"},
		Workflow: &contract.WorkflowSubscription{Script: serviceScript},
		Key:      "{repo}#{number}",
	})
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	beforeRun, err := w.store.ServiceRun(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}

	key := "owner/repo#7"
	if _, problem := w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub, Key: &key, Value: json.RawMessage(`{"head":"old"}`)}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub, Key: &key}); problem != nil {
		t.Fatal(problem)
	}

	// Record a github.ci.finished event before the update and advance the cursor past it.
	oldCI, err := w.event(w.room, engineSender, contract.CIFinished{Repo: "owner/repo", Number: 1, Result: contract.PrCISuccess})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvent(w.ctx, oldCI); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}

	updatedOn := []contract.EventType{"github.pr.changed", "github.ci.finished"}
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, On: &updatedOn}); problem != nil {
		t.Fatal(problem)
	}

	// Service run was not reloaded because the script did not change.
	afterRun, err := w.store.ServiceRun(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.ID != beforeRun.ID || afterRun.Version != beforeRun.Version {
		t.Fatalf("service run reloaded on on-only update: before=%+v after=%+v", beforeRun, afterRun)
	}

	// Dispatching again must not replay the old github.ci.finished event before the cursor.
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	var waiting int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=? AND run=''", sub).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != 1 {
		t.Fatalf("waiting=%d, want 1 (existing queued event kept, old history not replayed)", waiting)
	}

	// A new github.ci.finished event after the update reaches the service.
	newCI, err := w.event(w.room, engineSender, contract.CIFinished{Repo: "owner/repo", Number: 7, Result: contract.PrCIFailure})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvent(w.ctx, newCI); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}

	// First handover delivers the original queued event for key owner/repo#7 with its state intact.
	first, problem := w.WorkflowEvents(w.ctx, sessionCaller(afterRun.Session), contract.WorkflowEventsRequest{Boot: "boot", Limit: 10})
	if problem != nil || len(first.Events) != 1 || first.Events[0].Key != key {
		t.Fatalf("first handover: %+v %v", first, problem)
	}
	gotState, problem := w.WorkflowState(w.ctx, sessionCaller(afterRun.Session), contract.WorkflowStateRequest{Row: first.Events[0].Row})
	if problem != nil || string(gotState.Value) != `{"head":"old"}` {
		t.Fatalf("state after on update: %s %v", gotState.Value, problem)
	}
	if _, problem := w.WorkflowEventDone(w.ctx, sessionCaller(afterRun.Session), contract.WorkflowEventDoneRequest{Row: first.Events[0].Row}); problem != nil {
		t.Fatal(problem)
	}

	// Second handover delivers the newly added github.ci.finished event.
	second, problem := w.WorkflowEvents(w.ctx, sessionCaller(afterRun.Session), contract.WorkflowEventsRequest{Boot: "boot", Limit: 10})
	if problem != nil || len(second.Events) != 1 || second.Events[0].Key != key {
		t.Fatalf("second handover: %+v %v", second, problem)
	}
	var env struct {
		Type contract.EventType `json:"type"`
	}
	if err := json.Unmarshal(second.Events[0].Event, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != "github.ci.finished" {
		t.Fatalf("delivered event type %q, want github.ci.finished", env.Type)
	}
}

func TestSubscriptionUpdateEveryAddChangeAndRemoveAdjustsDueAt(t *testing.T) {
	w := setupWorkflow(t)
	_, clk := withRuns(t, w.Service)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{
		Scope:    contract.ScopeRoom,
		Room:     w.room,
		On:       []contract.EventType{"notice.posted"},
		Workflow: &contract.WorkflowSubscription{Script: serviceScript},
	})
	stored, err := w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.DueAt.IsZero() {
		t.Fatalf("event subscription has due_at %v", stored.DueAt)
	}

	// Add every (removing on): due_at is now + 5m.
	t0 := clk.now().UTC().Truncate(time.Millisecond)
	emptyOn := []contract.EventType{}
	fiveMin := "5m"
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, On: &emptyOn, Every: &fiveMin}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Every != "5m" || len(stored.On) != 0 || !stored.DueAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("after adding every: on=%v every=%q due=%v want=%v", stored.On, stored.Every, stored.DueAt, t0.Add(5*time.Minute))
	}
	info := listed(t, w.Service, w.ctx, w.room, sub)
	if info.NextAt == nil || !info.NextAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("listed nextAt = %v, want %v", info.NextAt, t0.Add(5*time.Minute))
	}

	// Updating another field without changing every preserves due_at.
	clk.add(2 * time.Minute)
	newKey := "fixed"
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Key: &newKey}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.DueAt.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("key-only update moved due_at to %v, want %v", stored.DueAt, t0.Add(5*time.Minute))
	}

	// Changing every to 10m at t0+2m moves due_at to t0+12m.
	tenMin := "600s"
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Every: &tenMin}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Every != "10m" || !stored.DueAt.Equal(t0.Add(12*time.Minute)) {
		t.Fatalf("after changing every: every=%q due=%v want=%v", stored.Every, stored.DueAt, t0.Add(12*time.Minute))
	}

	// Not due at t0+7m, due at t0+12m.
	clk.add(5 * time.Minute)
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=?", sub).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("fired early: queued=%d", queued)
	}
	clk.add(5 * time.Minute)
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=?", sub).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("did not fire at new due_at: queued=%d", queued)
	}

	// Remove every (restore on): due_at clears and no further schedule fires.
	noEvery := ""
	noticeOn := []contract.EventType{"notice.posted"}
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, On: &noticeOn, Every: &noEvery}); problem != nil {
		t.Fatal(problem)
	}
	stored, err = w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Every != "" || !stored.DueAt.IsZero() || !slices.Equal(stored.On, noticeOn) {
		t.Fatalf("after removing every: on=%v every=%q due=%v", stored.On, stored.Every, stored.DueAt)
	}
	info = listed(t, w.Service, w.ctx, w.room, sub)
	if info.NextAt != nil {
		t.Fatalf("removed schedule still lists nextAt=%v", info.NextAt)
	}
	clk.add(time.Hour)
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=?", sub).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("removed schedule still fired: queued=%d", queued)
	}
}

func TestSubscriptionUpdateOnAndEveryShapeRefusals(t *testing.T) {
	w := setupWorkflow(t)
	eventSub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{
		Scope:    contract.ScopeRoom,
		Room:     w.room,
		On:       []contract.EventType{"github.pr.changed"},
		Where:    []string{"kind=pushed"},
		Workflow: &contract.WorkflowSubscription{Script: serviceScript},
	})
	schedSub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{
		Scope:    contract.ScopeRoom,
		Room:     w.room,
		Every:    "5m",
		Workflow: &contract.WorkflowSubscription{Script: serviceScript},
	})

	emptyOn := []contract.EventType{}
	emptyWhere := []string{}
	emptyEvery := ""
	fiveMin := "5m"
	shortEvery := "30s"
	badEvery := "not-a-duration"
	noticeOn := []contract.EventType{"notice.posted"}
	unknownOn := []contract.EventType{"nope.event"}
	liveOn := []contract.EventType{"turn.activity"}
	dupOn := []contract.EventType{"github.pr.changed", "github.pr.changed"}

	for name, req := range map[string]contract.SubscriptionUpdateRequest{
		"workflow digest (add every with on)":    {ID: eventSub, Every: &fiveMin},
		"empty on without every":                 {ID: eventSub, On: &emptyOn},
		"non-filterable on with existing where":  {ID: eventSub, On: &noticeOn},
		"switch to every without clearing where": {ID: eventSub, On: &emptyOn, Every: &fiveMin},
		"unknown event type":                     {ID: eventSub, On: &unknownOn},
		"non-deliverable event type":             {ID: eventSub, On: &liveOn},
		"duplicate event type":                   {ID: eventSub, On: &dupOn},
		"every under a minute":                   {ID: eventSub, On: &emptyOn, Where: &emptyWhere, Every: &shortEvery},
		"invalid every duration":                 {ID: eventSub, On: &emptyOn, Where: &emptyWhere, Every: &badEvery},
		"clear every without setting on":         {ID: schedSub, Every: &emptyEvery},
		"workflow digest (add on with every)":    {ID: schedSub, On: &noticeOn},
	} {
		if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, req); problem == nil || problem.Code != contract.ErrInvalid {
			t.Errorf("%s: got %+v, want ErrInvalid", name, problem)
		}
	}

	gotEvent, err := w.store.SubscriptionByID(w.ctx, eventSub)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotEvent.On, []contract.EventType{"github.pr.changed"}) || gotEvent.Every != "" || !gotEvent.DueAt.IsZero() {
		t.Fatalf("refused updates mutated event subscription: %+v", gotEvent)
	}
	gotSched, err := w.store.SubscriptionByID(w.ctx, schedSub)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSched.On) != 0 || gotSched.Every != "5m" || gotSched.DueAt.IsZero() {
		t.Fatalf("refused updates mutated scheduled subscription: %+v", gotSched)
	}
}
