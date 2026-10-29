package approvals

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDisplayBodyUsesImmutableFileSnapshot(t *testing.T) {
	for _, argv := range [][]string{{"pr", "create", "--body-file", "body.md"}, {"pr", "create", "--body-file=body.md"}, {"pr", "create", "-Fbody.md"}} {
		payload := GHPayload{Argv: argv, Files: []FileSnapshot{{Path: "body.md", Data: []byte("approved body")}}}
		if got := payload.DisplayBody(); got != "approved body" {
			t.Fatalf("body = %q for %q", got, argv)
		}
	}
}

func TestApprovedRequestExecutesAndDeliversResult(t *testing.T) {
	f := newFixture(t)
	code := 0
	ran := make(chan Request, 1)
	f.service.Register(Kind("fake"), func(_ context.Context, r Request) (Result, error) {
		ran <- r
		return Result{ExitCode: &code, URL: "https://example.test/result", Output: []string{"created"}}, nil
	})
	r := f.create(t, ClassAgent)
	approved, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionApprove})
	if err != nil || approved.State != StateApproved {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	got, err := f.service.Wait(context.Background(), f.actor(f.child), f.rec.ID, r.ID, time.Second)
	if err != nil || got.State != StateDone || got.Result == nil {
		t.Fatalf("result = %+v, %v", got, err)
	}
	executed := <-ran
	if !reflect.DeepEqual(executed.Payload, r.Payload) || executed.PayloadHash != r.PayloadHash {
		t.Fatalf("executed payload = %+v, want %+v", executed, r)
	}
	if got.Result.Actor != f.parent.Address() || got.Result.URL != "https://example.test/result" || !reflect.DeepEqual(got.Result.Output, []string{"created"}) {
		t.Fatalf("stored result = %+v", got.Result)
	}
}

func TestApprovedRequestRunsOnceAfterRestart(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	restarted := New(f.rooms, f.service.SessionRoot)
	restarted.Now = f.service.Now
	calls := 0
	restarted.Register(Kind("fake"), func(_ context.Context, got Request) (Result, error) {
		calls++
		if got.ID != r.ID {
			t.Fatalf("executed %s, want %s", got.ID, r.ID)
		}
		return Result{}, nil
	})
	if err := restarted.RunApproved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunApproved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
	got, err := restarted.Get(f.actor(f.child), f.rec.ID, r.ID)
	if err != nil || got.State != StateDone || got.Result.Actor != f.parent.Address() {
		t.Fatalf("restarted result = %+v, %v", got, err)
	}
}

func TestInterruptedExecutionIsUncertainAndNeverReplays(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.markRunning(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	restarted := New(f.rooms, f.service.SessionRoot)
	restarted.Now = f.service.Now
	calls := 0
	restarted.Register(Kind("fake"), func(context.Context, Request) (Result, error) {
		calls++
		return Result{}, nil
	})
	if err := restarted.RunApproved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("interrupted write ran %d times", calls)
	}
	got, err := restarted.Get(f.actor(f.child), f.rec.ID, r.ID)
	if err != nil || got.State != StateFailed || !strings.Contains(got.Result.Reason, "uncertain") {
		t.Fatalf("reconciled request = %+v, %v", got, err)
	}
}

func TestExecutionFailureReachesRequester(t *testing.T) {
	f := newFixture(t)
	f.service.Register(Kind("fake"), func(context.Context, Request) (Result, error) {
		return Result{}, errors.New("write failed")
	})
	r := f.create(t, ClassAgent)
	if _, err := f.service.Decide(context.Background(), f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: r.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	got, err := f.service.Wait(context.Background(), f.actor(f.child), f.rec.ID, r.ID, time.Second)
	if err != nil || got.State != StateFailed || got.Result.Reason != "write failed" || got.Result.ExitCode == nil || *got.Result.ExitCode != 1 {
		t.Fatalf("failure = %+v, %v", got, err)
	}
}
