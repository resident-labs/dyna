package engine

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowSubscriptionSerialStateAndLatest(t *testing.T) {
	for _, latest := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "latest"}[latest], func(t *testing.T) {
			w := setupWorkflow(t)
			sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript, Args: map[string]json.RawMessage{"fixed": json.RawMessage(`42`)}}, Key: "one", Latest: latest})
			tick := func() {
				t.Helper()
				if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
					t.Fatal(err)
				}
				if err := w.fireDue(w.ctx); err != nil {
					t.Fatal(err)
				}
			}
			count := func() int {
				var n int
				if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_runs").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			recordNotice(t, w.Service, w.ctx, w.room)
			tick()
			if count() != 1 {
				t.Fatal("event did not start a run")
			}
			var run contract.WorkflowRunID
			if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM workflow_runs").Scan(&run); err != nil {
				t.Fatal(err)
			}
			stored, err := w.store.WorkflowRun(w.ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Subscription != sub || stored.Key != "one" {
				t.Fatalf("origin: %+v", stored)
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stored.Args), &args); err != nil {
				t.Fatal(err)
			}
			if string(args["fixed"]) != "42" || !strings.Contains(string(args["event"]), "notice.posted") {
				t.Fatalf("args %s", stored.Args)
			}
			principal := sessionCaller(stored.Session)
			if _, problem := w.WorkflowState(w.ctx, principal, contract.WorkflowStateRequest{Value: json.RawMessage(`{"head":"abc"}`)}); problem != nil {
				t.Fatal(problem)
			}
			if _, problem := w.WorkflowState(w.ctx, principal, contract.WorkflowStateRequest{Value: json.RawMessage(`"` + strings.Repeat("x", 65536) + `"`)}); problem == nil {
				t.Fatal("unbounded state")
			}
			recordNotice(t, w.Service, w.ctx, w.room)
			tick()
			recordNotice(t, w.Service, w.ctx, w.room)
			tick()
			if count() != 1 {
				t.Fatal("overlapping runs")
			}
			var pending int
			if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE run=''").Scan(&pending); err != nil {
				t.Fatal(err)
			}
			want := 2
			if latest {
				want = 1
			}
			if pending != want {
				t.Fatalf("pending %d want %d", pending, want)
			}
			if err := w.store.Write(w.ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(w.ctx, "UPDATE workflow_runs SET state='done' WHERE id=?", run)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			tick()
			if count() != 2 {
				t.Fatal("second event did not start")
			}
			var next contract.WorkflowRunID
			if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM workflow_runs WHERE state='running'").Scan(&next); err != nil {
				t.Fatal(err)
			}
			second, err := w.store.WorkflowRun(w.ctx, next)
			if err != nil {
				t.Fatal(err)
			}
			var latestSeq int64
			if err := w.store.Read().QueryRowContext(w.ctx, "SELECT max(seq) FROM events WHERE type='notice.posted'").Scan(&latestSeq); err != nil {
				t.Fatal(err)
			}
			var secondArgs struct {
				Event struct {
					Seq int64 `json:"seq"`
				} `json:"event"`
			}
			if err := json.Unmarshal([]byte(second.Args), &secondArgs); err != nil {
				t.Fatal(err)
			}
			if latest && secondArgs.Event.Seq != latestSeq || !latest && secondArgs.Event.Seq >= latestSeq {
				t.Fatalf("queued event %d, latest %d, latest %v", secondArgs.Event.Seq, latestSeq, latest)
			}
			state, problem := w.WorkflowState(w.ctx, sessionCaller(second.Session), contract.WorkflowStateRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			if string(state.Value) != `{"head":"abc"}` {
				t.Fatalf("state %s", state.Value)
			}
		})
	}
}

func TestWorkflowSubscriptionOwnCommentAndPush(t *testing.T) {
	w := setupWorkflow(t)
	for _, kind := range []string{"comment", "pushed"} {
		addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.pr.changed"}, Where: []string{"kind=" + kind}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}, Key: "{repo}/{number}"})
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
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("own comment started a run")
	}
	record(contract.PrChangeComment, contract.PrChangePushed)
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("own push: %d runs; comment must not match", n)
	}
	var key string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT key FROM workflow_runs").Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "owner/repo/7" {
		t.Fatalf("key %q", key)
	}
}

func TestScheduledWorkflowUsesNormalRunAndState(t *testing.T) {
	w := setupWorkflow(t)
	_, clock := withRuns(t, w.Service)
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, Every: "1m", Workflow: &contract.WorkflowSubscription{Script: workflowScript}})
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Minute)
	if err := w.fireDue(w.ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("scheduled runs: %d", n)
	}
}

func TestWorkflowSubscriptionSplitsBatchedPRsByKey(t *testing.T) {
	w := setupWorkflow(t)
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.pr.changed"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}, Key: "{repo}/{number}"})
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
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(DISTINCT key) FROM workflow_runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("only %d keys started", n)
	}
}

func TestWorkflowSubscriptionIssueKeysAndOwnComment(t *testing.T) {
	w := setupWorkflow(t)
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"github.issue.changed"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}, Key: "{repo}/{number}"})
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
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT key FROM workflow_runs").Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "owner/repo/8" {
		t.Fatalf("outside issue key %q", key)
	}
}

func TestWorkflowStartRefusalConsumesEventAndBacksOffOnce(t *testing.T) {
	w := setupWorkflow(t)
	_, clock := withRuns(t, w.Service)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}})
	w.set(w.room, contract.WorkflowSettings{Start: contract.WorkflowStartOff})
	tick := func() {
		t.Helper()
		if _, err := w.store.DispatchEvents(w.ctx, 1000); err != nil {
			t.Fatal(err)
		}
		if err := w.fireDue(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	recordNotice(t, w.Service, w.ctx, w.room)
	tick()
	for range 3 {
		tick()
	}
	failed := eventsOf[contract.HookFinished](t, w.Service, w.ctx, w.room)
	if len(failed) != 1 || failed[0].Failures != 1 {
		t.Fatalf("refusals %+v", failed)
	}
	stored, err := w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Failures != 1 || !stored.HoldUntil.After(clock.now()) {
		t.Fatalf("backoff %+v", stored)
	}
	var starting int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM workflow_queue WHERE run='starting'").Scan(&starting); err != nil {
		t.Fatal(err)
	}
	if starting != 0 {
		t.Fatal("refused event still starting")
	}
	clock.add(time.Minute)
	tick()
	failed = eventsOf[contract.HookFinished](t, w.Service, w.ctx, w.room)
	if len(failed) != 2 || failed[1].Failures != 2 {
		t.Fatalf("second refusal %+v", failed)
	}
	var notices int
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(*) FROM events WHERE type='notice.posted' AND CAST(body AS TEXT) LIKE '%could not start%'").Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("failure notices %d", notices)
	}
	recordNotice(t, w.Service, w.ctx, w.room)
	w.set(w.room, contract.WorkflowSettings{Start: contract.WorkflowStartAny})
	clock.add(2 * time.Minute)
	tick()
	stored, err = w.store.SubscriptionByID(w.ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Failures != 0 || !stored.HoldUntil.IsZero() {
		t.Fatal("successful start did not reset backoff")
	}
}

func TestWorkflowClosedParentFallsBackToRoot(t *testing.T) {
	w := setupWorkflow(t)
	var parent contract.SessionID
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM sessions WHERE name='worker' AND room=?", w.room).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, Parent: parent, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}})
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
