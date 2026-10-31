package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestStopPersistsAcrossReconnect(t *testing.T) {
	s, _, session, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	ch, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	result, problem := s.SessionStop(ctx, owner, contract.SessionStopRequest{Session: session})
	if problem != nil || !result.Stopping {
		t.Fatalf("stop: %+v %v", result, problem)
	}
	stop := (<-ch).Body.(contract.TurnStop)
	if stop.Turn != start.Turn {
		t.Fatalf("stop: %+v", stop)
	}
	ch2, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	replay := (<-ch2).Body.(contract.TurnStart)
	repeated := (<-ch2).Body.(contract.TurnStop)
	if replay.Turn != start.Turn || repeated.Turn != start.Turn {
		t.Fatalf("replay: %+v %+v", replay, repeated)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnStopped}); problem != nil {
		t.Fatal(problem)
	}
	result, problem = s.SessionStop(ctx, owner, contract.SessionStopRequest{Session: session})
	if problem != nil || result.Stopping {
		t.Fatalf("idle stop: %+v %v", result, problem)
	}
}

func TestPauseHoldsDeliveryUntilResume(t *testing.T) {
	s, room, session, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	ch, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	if _, problem := s.RoomPause(ctx, owner, contract.RoomPauseRequest{Room: room, Paused: true}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "@worker hello", Mentions: []string{"worker"}}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	var pending int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND state='pending' AND wake=1", session).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	if _, problem := s.RoomPause(ctx, owner, contract.RoomPauseRequest{Room: room, Paused: false}); problem != nil {
		t.Fatal(problem)
	}
	next := (<-ch).Body.(contract.TurnStart)
	if next.Turn == start.Turn || len(next.Inputs) != 1 || next.Inputs[0].Text != "@worker hello" {
		t.Fatalf("resumed: %+v", next)
	}
}
