package engine

import (
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestSubscriptionFireUpdateAndStateKeepQueueAndState(t *testing.T) {
	w := setupWorkflow(t)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}, Key: "original"})
	key := "pr-1"
	state, problem := w.SubscriptionState(w.ctx, w.owner, contract.SubscriptionStateRequest{ID: sub, Key: &key, Value: json.RawMessage(`{"head":"old"}`)})
	if problem != nil || len(state.States) != 1 || string(state.States[0].Value) != `{"head":"old"}` {
		t.Fatalf("state %+v %v", state, problem)
	}
	if _, problem := w.SubscriptionFire(w.ctx, w.owner, contract.SubscriptionFireRequest{ID: sub, Key: &key}); problem != nil {
		t.Fatal(problem)
	}
	changedKey := "new-template"
	latest := true
	parent := w.root
	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{ID: sub, Workflow: &contract.WorkflowSubscription{Script: workflowScript, Args: map[string]json.RawMessage{"updated": json.RawMessage("true")}}, Key: &changedKey, Latest: &latest, Parent: &parent}); problem != nil {
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
	var run contract.WorkflowRunID
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM workflow_runs WHERE sub=?", sub).Scan(&run); err != nil {
		t.Fatal(err)
	}
	active, err := w.store.WorkflowRun(w.ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if active.Key != key {
		t.Fatal("fire did not keep literal key")
	}
	got, problem := w.WorkflowState(w.ctx, sessionCaller(active.Session), contract.WorkflowStateRequest{})
	if problem != nil || string(got.Value) != `{"head":"old"}` {
		t.Fatalf("real state %s %v", got.Value, problem)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(active.Args), &args); err != nil {
		t.Fatal(err)
	}
	if string(args["updated"]) != "true" {
		t.Fatal("queued run did not use updated script args")
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
	busy, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript})
	if problem != nil {
		t.Fatal(problem)
	}
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}, Latest: true})
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
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.pr.changed"}, Where: []string{"kind=pushed"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}, Key: "{repo}#{number}"})
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
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}})
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
