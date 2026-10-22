package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
)

func postClaimInput(t *testing.T, e *testEnv, info Info, text string) room.Message {
	t.Helper()
	msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: info.Room, Human: true, To: []string{info.Address()}, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestHeldPromptLeavesNewInputAndCoveredNotesUnclaimed(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	msg := postClaimInput(t, e, info, "new input")
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) { turn.HeldPrompt = "replay this exact prompt" }); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	got, err := e.m.RunnerClaimInput(ctx, InputClaim{Turn: 1, Startup: true, Covered: []string{msg.ID}})
	if err != nil || len(got.Posts) != 0 || got.Cursor != info.RoomCursor {
		t.Fatalf("held prompt claim = %+v, %v", got, err)
	}
	dir, _ := e.m.Store.Dir(info.ID)
	q, err := readQueue(dir)
	if err != nil || q.Claimed[msg.ID] != 0 || q.Cancelled[msg.ID] {
		t.Fatalf("held prompt changed the new input: %+v, %v", q, err)
	}
}

func TestStartupAndLiveInputUseTheSameClaimBoundary(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	cancelled := postClaimInput(t, e, info, "covered note")
	dismissed := postClaimInput(t, e, info, "dismissed")
	accepted := postClaimInput(t, e, info, "startup input")
	dir, _ := e.m.Store.Dir(info.ID)
	q, _ := readQueue(dir)
	q.Dismissed[dismissed.ID] = true
	if err := writeQueue(dir, q); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	calls := e.m
	first, err := calls.RunnerClaimInput(ctx, InputClaim{Turn: 1, Startup: true, Covered: []string{cancelled.ID}})
	if err != nil || len(first.Posts) != 1 || first.Posts[0].Steer.ID != accepted.ID {
		t.Fatalf("startup claim = %+v, %v; want only %s", first, err, accepted.ID)
	}
	gate := e.m.Store.newTurnGate(e.m.rooms(), info.Meta, 1, first.Cursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	live := postClaimInput(t, e, info, "live input")
	last, err := calls.RunnerClaimInput(ctx, InputClaim{Turn: 1, Cursor: first.Cursor, Depth: info.Depth, ParentSession: info.ParentSession})
	if err != nil || len(last.Posts) != 1 || last.Posts[0].Steer.ID != live.ID {
		t.Fatalf("live claim = %+v, %v; want only %s", last, err, live.ID)
	}
	q, err = readQueue(dir)
	if err != nil || q.Claimed[accepted.ID] != 1 || q.Claimed[live.ID] != 1 || !q.Cancelled[cancelled.ID] || q.Claimed[cancelled.ID] != 0 || q.Claimed[dismissed.ID] != 0 {
		t.Fatalf("stored claims = %+v, %v", q, err)
	}
}

func TestClaimReplySurvivesDatabaseReopenBeforeFinishRequest(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	msg := postClaimInput(t, e, info, "accepted once")
	dataDir := filepath.Dir(e.m.Store.root())
	if err := state.MigrateRequests(dataDir); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	rid := info.ID + "/claim/1"
	request := func() context.Context { return state.ForRequest(callspec.WithRequest(actor, rid), "runner/"+rid) }
	calls := e.m
	reply, err := calls.RunnerClaimInput(request(), InputClaim{Turn: 1, Startup: true})
	if err != nil || len(reply.Posts) != 1 || reply.Posts[0].Steer.ID != msg.ID {
		t.Fatalf("claim = %+v, %v", reply, err)
	}
	want, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	saved, done, err := state.For(db).RequestDone(t.Context(), "runner/"+rid)
	if err != nil || !done || string(saved) != string(want) {
		t.Fatalf("saved reply = %s, done %v, %v; want %s", saved, done, err, want)
	}
	if _, err := calls.RunnerClaimInput(request(), InputClaim{Turn: 1, Startup: true}); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed claim = %v; want ErrRepeated with the saved reply", err)
	}
}

func TestClaimRefusesAnotherTurnOrAnUnidentifiedCaller(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	msg := postClaimInput(t, e, info, "keep waiting")
	calls := e.m
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	for _, input := range []InputClaim{{Turn: 0, Startup: true}, {Turn: 2, Startup: true}, {Turn: 1, Startup: true, Final: true}} {
		if _, err := calls.RunnerClaimInput(actor, input); err == nil {
			t.Fatalf("accepted invalid input claim: %+v", input)
		}
	}
	if _, err := calls.RunnerClaimInput(t.Context(), InputClaim{Turn: 1, Startup: true}); err == nil {
		t.Fatal("an unidentified caller claimed input")
	}
	dir, _ := e.m.Store.Dir(info.ID)
	if q, err := readQueue(dir); err != nil || q.Claimed[msg.ID] != 0 {
		t.Fatalf("refused input was claimed: %+v, %v", q, err)
	}
}

func TestInputSelectorFramesDoNotRepairOtherTurns(t *testing.T) {
	e := newTestEnv(t)
	parent := completedFixtureSession(t, e)
	child := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateTurn(parent.ID, 1, func(turn *Turn) { turn.Status = TurnRunning }); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.UpdateMeta(child.ID, func(meta *Meta) { meta.ParentSession, meta.Depth = parent.ID, 2 })
	if err != nil {
		t.Fatal(err)
	}
	_ = e.m.Store.SelectorContext(meta)
	if turn, err := e.m.Store.ReadTurn(parent.ID, 1); err != nil || turn.Status != TurnRunning {
		t.Fatalf("a selector frame repaired the parent: status %s, %v", turn.Status, err)
	}
}
