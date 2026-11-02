package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The box loop looks and then dispatches. This is the write after a wake or a job's end landed between the two, with no box
// loop running, so nothing else can stop the turn.
func TestStaleDispatchOpensTurnOnlyForBoxSomethingNeeds(t *testing.T) {
	s, _, room, sid, ctx := staleSession(t)
	endTurn(t, s, ctx, sid, openTurn(t, s, ctx, room, sid, "turn_job"), testJob)
	dispatch := func() contract.TurnID {
		t.Helper()
		started, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
		if err != nil {
			t.Fatal(err)
		}
		turn, _, _, err := s.store.DispatchStale(ctx, sid, "turn_late", started, false)
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
	if _, problem := s.ShellJobEnded(ctx, boxCaller(sid), contract.ShellJobEndedRequest{Job: testJob, End: jobExited, Tail: "built"}); problem != nil {
		t.Fatal(problem)
	}
	turns := turnCount(t, s, ctx, sid)
	if turn := dispatch(); turn != "" {
		t.Fatalf("turn %s opened on a box nothing needs", turn)
	}
	if got := turnCount(t, s, ctx, sid); got != turns {
		t.Fatalf("turns=%d, want %d", got, turns)
	}
	var pending int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND wake=1 AND state='pending'", sid).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending wakes=%d: %v", pending, err)
	}
	handle, problem := s.createSession(ctx, sid)
	if problem != nil {
		t.Fatal(problem)
	}
	if !handle.OpenTerminal() {
		t.Fatal("terminal did not open")
	}
	defer handle.CloseTerminal()
	if turn := dispatch(); turn != "turn_late" {
		t.Fatalf("turn=%q under a terminal", turn)
	}
}
