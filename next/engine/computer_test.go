package engine

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
)

func TestComputerInputReachesBoxAndRefusesSessions(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	boxCtx, stopBox := context.WithCancel(ctx)
	defer stopBox()
	messages, err := s.BoxChannel(boxCtx, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	req := contract.ComputerInputRequest{Session: id, Frame: contract.FrameSize{Width: 10, Height: 10}, Events: []contract.ComputerInput{{Kind: contract.InputKeyDown, Key: "a"}}}
	if _, err := s.ComputerInput(ctx, owner, req); err == nil || err.Code != contract.ErrNotFound {
		t.Fatalf("stopped computer: %v", err)
	}
	if _, err := s.ComputerChanged(ctx, caller, contract.ComputerChangedRequest{To: contract.ComputerRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ComputerInput(ctx, caller, req); err == nil || err.Code != contract.ErrForbidden {
		t.Fatalf("session caller: %v", err)
	}
	if _, err := s.ComputerInput(ctx, owner, req); err != nil {
		t.Fatal(err)
	}
	for m := range messages {
		if batch, ok := m.Body.(contract.ComputerInputBatch); ok {
			if batch.Frame != req.Frame || batch.Events[0].Key != "a" {
				t.Fatalf("batch %+v", batch)
			}
			return
		}
	}
	t.Fatal("box channel closed before computer.input")
}

// A replaced box rejects input even when its queue has room.
func TestComputerInputReplacement(t *testing.T) {
	done := make(chan struct{})
	channel := live.NewChannel(func() {}, done)
	close(done)
	problem := channel.SendInput(context.Background(), contract.ComputerInputBatch{})
	if problem == nil || problem.Code != contract.ErrConflict || problem.Message != "box replaced" {
		t.Fatalf("replacement: %v", problem)
	}
}
