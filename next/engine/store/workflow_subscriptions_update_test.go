package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestUpdateWorkflowSubscriptionOnAndEveryKeepsStateAndQueue(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	if _, err := s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0)`); err != nil {
		t.Fatal(err)
	}
	sub := contract.Subscription{
		ID:       "sub_wf",
		Scope:    contract.ScopeRoom,
		Room:     "r",
		On:       []contract.EventType{"github.pr.changed"},
		Workflow: &contract.WorkflowSubscription{Script: "serve(() => {})"},
		Key:      "{repo}#{number}",
	}
	if err := s.AddSubscription(ctx, sub, t0); err != nil {
		t.Fatal(err)
	}

	key := "owner/repo#7"
	if _, err := s.ManageSubscriptionState(ctx, sub.ID, &key, json.RawMessage(`{"head":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.SubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.FireSubscriptionWorkflow(ctx, stored, key, contract.Event{
		ID:   "ev_1",
		Room: "r",
		Time: t0,
		From: contract.Principal{Kind: contract.PrincipalOwner},
		Body: contract.HookDue{Subscription: sub.ID},
	})
	if err != nil || !queued {
		t.Fatalf("queue event: queued=%v err=%v", queued, err)
	}

	// Update on: add a type; state, queue and zero due_at must stay untouched.
	t1 := t0.Add(time.Minute)
	sub.On = []contract.EventType{"github.pr.changed", "github.ci.finished"}
	if err := s.UpdateWorkflowSubscription(ctx, sub, t1); err != nil {
		t.Fatal(err)
	}
	got, err := s.SubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.On, []contract.EventType{"github.ci.finished", "github.pr.changed"}) {
		t.Fatalf("on = %v", got.On)
	}
	if got.Every != "" || !got.DueAt.IsZero() {
		t.Fatalf("expected no schedule, got every=%q due=%v", got.Every, got.DueAt)
	}
	states, err := s.ManageSubscriptionState(ctx, sub.ID, &key, nil)
	if err != nil || len(states) != 1 || string(states[0].Value) != `{"head":"abc"}` {
		t.Fatalf("state lost across on update: %+v %v", states, err)
	}
	if got.Waiting != 1 {
		t.Fatalf("waiting = %d, want 1", got.Waiting)
	}

	// Add every (switching from on to schedule): due_at is t2 + 5m.
	t2 := t1.Add(time.Minute)
	sub.On = nil
	sub.Every = "5m"
	if err := s.UpdateWorkflowSubscription(ctx, sub, t2); err != nil {
		t.Fatal(err)
	}
	got, err = s.SubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.On) != 0 || got.Every != "5m" || !got.DueAt.Equal(t2.Add(5*time.Minute)) {
		t.Fatalf("after adding every: on=%v every=%q due=%v want=%v", got.On, got.Every, got.DueAt, t2.Add(5*time.Minute))
	}

	// Unchanged every (e.g. changing only key later) keeps existing due_at.
	t3 := t2.Add(2 * time.Minute)
	sub.Key = "other"
	if err := s.UpdateWorkflowSubscription(ctx, sub, t3); err != nil {
		t.Fatal(err)
	}
	got, err = s.SubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.DueAt.Equal(t2.Add(5 * time.Minute)) {
		t.Fatalf("unchanged every moved due_at to %v, want %v", got.DueAt, t2.Add(5*time.Minute))
	}

	// Change every to 15m: due_at becomes t3 + 15m.
	sub.Every = "15m"
	if err := s.UpdateWorkflowSubscription(ctx, sub, t3); err != nil {
		t.Fatal(err)
	}
	got, err = s.SubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Every != "15m" || !got.DueAt.Equal(t3.Add(15*time.Minute)) {
		t.Fatalf("after changing every: every=%q due=%v want=%v", got.Every, got.DueAt, t3.Add(15*time.Minute))
	}

	// Remove every (switch back to on): due_at clears to zero and nothing is due.
	t4 := t3.Add(time.Minute)
	sub.Every = ""
	sub.On = []contract.EventType{"github.ci.finished"}
	if err := s.UpdateWorkflowSubscription(ctx, sub, t4); err != nil {
		t.Fatal(err)
	}
	got, err = s.SubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Every != "" || !got.DueAt.IsZero() || !slices.Equal(got.On, []contract.EventType{"github.ci.finished"}) {
		t.Fatalf("after removing every: on=%v every=%q due=%v", got.On, got.Every, got.DueAt)
	}
	due, err := s.DueSubscriptions(ctx, t4.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("removed schedule still due: %+v", due)
	}

	// State and queue survived every transition.
	states, err = s.ManageSubscriptionState(ctx, sub.ID, &key, nil)
	if err != nil || len(states) != 1 || string(states[0].Value) != `{"head":"abc"}` {
		t.Fatalf("state lost across transitions: %+v %v", states, err)
	}
	if got.Waiting != 1 {
		t.Fatalf("waiting = %d after transitions, want 1", got.Waiting)
	}

	// Missing subscription returns ErrNotFound.
	missing := sub
	missing.ID = "sub_missing"
	if err := s.UpdateWorkflowSubscription(ctx, missing, t4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing subscription: got %v, want ErrNotFound", err)
	}
}
