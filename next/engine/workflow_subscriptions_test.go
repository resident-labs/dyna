package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowSubscriptionOwnCommentAndPush(t *testing.T) {
	w := setupWorkflow(t)
	for _, kind := range []string{"comment", "pushed"} {
		addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.pr.changed"}, Where: []string{"kind=" + kind}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Key: "{repo}/{number}"})
	}
	record := func(kinds ...contract.PrChangeKind) {
		t.Helper()
		event, err := w.event(w.room, engineSender, contract.PrChanged{Changes: []contract.PrChange{{PR: contract.PrSummary{Repo: "owner/repo", Number: 7}, Kinds: kinds, OwnKinds: []contract.PrChangeKind{contract.PrChangeComment}, Comments: []contract.PrComment{{Author: "own", Body: "own review", Outside: false}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.store.RecordEvent(w.ctx, event); err != nil {
			t.Fatal(err)
		}
		if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
			t.Fatal(err)
		}
		if err := w.fireDue(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	record(contract.PrChangeComment)
	var n int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("own comment started a run")
	}
	record(contract.PrChangeComment, contract.PrChangePushed)
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("own push: %d runs; comment must not match", n)
	}
	var key string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT key FROM workflow_queue").Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "owner/repo/7" {
		t.Fatalf("key %q", key)
	}
}

func TestScheduledWorkflowUsesNormalRunAndState(t *testing.T) {
	w := setupWorkflow(t)
	_, clock := withRuns(t, w.Service)
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, Every: "1m", Workflow: &contract.WorkflowSubscription{Script: serviceScript}})
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Minute)
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("scheduled runs: %d", n)
	}
}

func TestWorkflowSubscriptionSplitsBatchedPRsByKey(t *testing.T) {
	w := setupWorkflow(t)
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.pr.changed"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Key: "{repo}/{number}"})
	event, err := w.event(w.room, engineSender, contract.PrChanged{Changes: []contract.PrChange{
		{PR: contract.PrSummary{Repo: "owner/repo", Number: 7}, Kinds: []contract.PrChangeKind{contract.PrChangePushed}},
		{PR: contract.PrSummary{Repo: "owner/repo", Number: 8}, Kinds: []contract.PrChangeKind{contract.PrChangePushed}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvent(w.ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(DISTINCT key) FROM workflow_queue").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("only %d keys started", n)
	}
}

func TestWorkflowSubscriptionIssueKeysAndOwnComment(t *testing.T) {
	w := setupWorkflow(t)
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.issue.changed"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Key: "{repo}/{number}"})
	event, err := w.event(w.room, engineSender, contract.IssueChanged{Changes: []contract.IssueChange{
		{Issue: contract.IssueSummary{Repo: "owner/repo", Number: 7}, Kinds: []contract.PrChangeKind{contract.PrChangeComment}, Comments: []contract.PrComment{{Outside: false}}},
		{Issue: contract.IssueSummary{Repo: "owner/repo", Number: 8}, Kinds: []contract.PrChangeKind{contract.PrChangeComment}, Comments: []contract.PrComment{{Outside: true}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordEvent(w.ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT key FROM workflow_queue").Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "owner/repo/8" {
		t.Fatalf("outside issue key %q", key)
	}
}

func TestWorkflowClosedParentFallsBackToRoot(t *testing.T) {
	w := setupWorkflow(t)
	var parent contract.SessionID
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM sessions WHERE name='worker' AND room=?", w.room).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, Parent: parent, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}})
	closed, err := w.event(w.room, engineSender, contract.SessionClosed{Session: parent})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.CloseSession(w.ctx, parent, closed); err != nil {
		t.Fatal(err)
	}
	recordNotice(t, w.Service, w.ctx, w.room)
	if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT s.parent FROM sessions s JOIN workflow_runs r ON r.session=s.id").Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != w.root {
		t.Fatalf("run parent %s", parent)
	}
}
