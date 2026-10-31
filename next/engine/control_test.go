package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestStopPersistsAcrossReconnect(t *testing.T) {
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
	var read int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND turn=? AND state='read'", session, start.Turn).Scan(&read); err != nil || read != 1 {
		t.Fatalf("read inputs=%d: %v", read, err)
	}
	result, problem = s.SessionStop(ctx, owner, contract.SessionStopRequest{Session: session})
	if problem != nil || result.Stopping {
		t.Fatalf("idle stop: %+v %v", result, problem)
	}
}

func TestControlsEnforceAuthority(t *testing.T) {
	s, room, worker, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if _, problem := s.RoomPause(ctx, caller, contract.RoomPauseRequest{Room: room, Paused: true}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("worker paused room: %v", problem)
	}
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.SessionStop(ctx, caller, contract.SessionStopRequest{Session: root}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("worker stopped ancestor: %v", problem)
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
	if next.Turn == start.Turn || len(next.Inputs) != 1 || inputBody(next.Inputs[0].Text) != "@worker hello" {
		t.Fatalf("resumed: %+v", next)
	}
}

func TestRoomSetLimits(t *testing.T) {
	s, room, session, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	req := contract.RoomSetLimitsRequest{Room: room, CPUs: 2, MemBytes: 8 << 30}
	if _, problem := s.RoomSetLimits(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", problem)
	}
	if _, problem := s.RoomSetLimits(ctx, owner, contract.RoomSetLimitsRequest{Room: room, CPUs: -1}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("negative: %v", problem)
	}
	if _, problem := s.RoomSetLimits(ctx, owner, contract.RoomSetLimitsRequest{Room: room, MemBytes: 1 << 62}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("overflow: %v", problem)
	}
	if _, problem := s.RoomSetLimits(ctx, owner, req); problem != nil {
		t.Fatal(problem)
	}
	var cpus int
	var mem int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT cpus,mem_bytes FROM rooms WHERE id=?", room).Scan(&cpus, &mem); err != nil || cpus != req.CPUs || mem != req.MemBytes {
		t.Fatalf("limits %d %d: %v", cpus, mem, err)
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"room.limits_changed"})
	if err != nil || len(events) != 1 || events[0].Body != (contract.RoomLimitsChanged{CPUs: req.CPUs, MemBytes: req.MemBytes}) {
		t.Fatalf("events %+v: %v", events, err)
	}
	if _, problem := s.RoomSetLimits(ctx, owner, contract.RoomSetLimitsRequest{Room: room}); problem != nil {
		t.Fatal(problem)
	}
	if err := s.store.Read().QueryRowContext(ctx, "SELECT cpus,mem_bytes FROM rooms WHERE id=?", room).Scan(&cpus, &mem); err != nil || cpus != 0 || mem != 0 {
		t.Fatalf("unlimited %d %d: %v", cpus, mem, err)
	}
}
