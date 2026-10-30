package approvals

import (
	"context"
	"errors"
	"testing"

	"dyna-agent/internal/authority"
)

type testPayload struct{ Class Class }

func TestSubmitRoutesAndAudits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	runs := make(chan struct{}, 4)
	Register(f.service, Operation[testPayload]{
		Kind: "test.write",
		Decide: func(_ context.Context, _ authority.Actor, p testPayload) (Decision, error) {
			if p.Class == "" {
				return Decision{}, errors.New("refused")
			}
			return Decision{Class: p.Class}, nil
		},
		Card:  func(testPayload) Card { return Card{Repository: "test/repo"} },
		Check: func(context.Context, authority.Actor, testPayload) error { return nil },
		Execute: func(context.Context, authority.Actor, testPayload) (Result, error) {
			runs <- struct{}{}
			return Result{}, nil
		},
	})
	for _, tc := range []struct {
		actor authority.Actor
		class Class
	}{
		{f.actor(f.root), ClassAgent}, {f.actor(f.child), ClassAuto},
	} {
		out, err := Submit(ctx, f.service, tc.actor, "test.write", testPayload{tc.class})
		if err != nil || out.Request.ID != "" {
			t.Fatalf("inline = %+v, %v", out, err)
		}
		<-runs
	}
	msgs, err := f.rooms.Messages(f.rec.ID)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("inline messages = %+v, %v", msgs, err)
	}
	db, err := f.service.db()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM approval_requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("inline request count = %d, %v", count, err)
	}
	held, err := Submit(ctx, f.service, f.actor(f.child), "test.write", testPayload{ClassAgent})
	if err != nil || held.Request.At != "parent" {
		t.Fatalf("held = %+v, %v", held, err)
	}
	if _, err := f.service.Decide(ctx, f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: held.Request.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	<-runs
	f.service.inflight.Wait()
	owner, err := Submit(ctx, f.service, f.actor(f.root), "test.write", testPayload{ClassOwner})
	if err != nil || owner.Request.At != Owner {
		t.Fatalf("owner = %+v, %v", owner, err)
	}
	if _, err := Submit(ctx, f.service, f.actor(f.root), "test.write", testPayload{}); err == nil {
		t.Fatal("refusal succeeded")
	}
}
