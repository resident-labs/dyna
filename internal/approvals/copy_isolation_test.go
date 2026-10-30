package approvals

import (
	"context"
	"testing"
	"time"
)

func TestCopiedApprovalDoesNotExecute(t *testing.T) {
	f := newFixture(t)
	old := f.create(t, ClassAgent)
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: old.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	copiedAt := f.now.Add(time.Second)
	f.now = copiedAt.Add(time.Second)
	fresh := f.createAs(t, f.actor(f.sibling), ClassAgent)
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: fresh.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	restarted := New(f.rooms, f.service.SessionRoot)
	restarted.Now = f.service.Now
	restarted.CopyTime = copiedAt
	var calls []string
	restarted.Register(Kind("fake"), func(_ context.Context, r Request) (Result, error) {
		calls = append(calls, r.ID)
		return Result{}, nil
	})
	if err := restarted.RunApproved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != fresh.ID {
		t.Fatalf("executed approvals = %v; want only %s", calls, fresh.ID)
	}
}
