package engine

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowSubscriptionSerialStateAndOnce(t *testing.T) {
	for _, once := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "latest"}[once], func(t *testing.T) {
			w := setupWorkflow(t)
			sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript, Args: map[string]json.RawMessage{"fixed": json.RawMessage(`42`)}}, Key: "one", Once: once})
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
			if stored.Subscription != sub || stored.Subject != "one" {
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
			if once {
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
			var latest int64
			if err := w.store.Read().QueryRowContext(w.ctx, "SELECT max(seq) FROM events WHERE type='notice.posted'").Scan(&latest); err != nil {
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
			if once && secondArgs.Event.Seq != latest || !once && secondArgs.Event.Seq >= latest {
				t.Fatalf("queued event %d, latest %d, once %v", secondArgs.Event.Seq, latest, once)
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
		event, err := w.event(w.room, engineSender, contract.PrChanged{Changes: []contract.PrChange{{PR: contract.PrSummary{Repo: "owner/repo", Number: 7}, Kinds: kinds, Comments: []contract.PrComment{{Author: "own", Body: "own review", Outside: false}}}}})
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
	var subject string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT subject FROM workflow_origins").Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if subject != "owner/repo/7" {
		t.Fatalf("key %q", subject)
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
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT count(DISTINCT subject) FROM workflow_origins").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("only %d subjects started", n)
	}
}
