package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/state"
)

func TestWaitSessionRecordsPromptThroughTheEngine(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	type pendingCall struct {
		id   string
		args []map[string]json.RawMessage
	}
	calls := make(chan pendingCall, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args []map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/api/RunnerTurnNote" {
			t.Errorf("method = %s, want RunnerTurnNote", r.URL.Path)
		}
		calls <- pendingCall{id: r.Header.Get(callspec.RequestHeader), args: args}
		_, _ = w.Write([]byte("null"))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	e.m.Engine = engineclient.New(path)
	writes := e.m.runnerWrites(info.ID, 1)
	t.Cleanup(func() { writes.close(context.Background()) })
	const prompt = "continue this exact input"
	if err := writes.waitSession(t.Context(), prompt); err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.HeldPrompt != "" {
		t.Fatalf("runner changed the local held prompt to %q: %v", turn.HeldPrompt, err)
	}
	select {
	case call := <-calls:
		if len(call.args) != 1 || string(call.args[0]["pendingPrompt"]) != `"`+prompt+`"` || string(call.args[0]["turn"]) != "1" || !strings.HasPrefix(call.id, info.ID+"/") {
			t.Fatalf("pending prompt call = %+v", call)
		}
	default:
		t.Fatal("pending prompt never reached the engine")
	}
}

func TestHeldInputNotesPreserveAcceptedInputUntilItChanges(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	for _, step := range []struct {
		note           TurnNote
		prompt         string
		accepted, held bool
	}{
		{TurnNote{PendingPrompt: "original"}, "original", false, false},
		{TurnNote{Accepted: true}, "original", true, false},
		{TurnNote{PendingPrompt: "original"}, "original", true, false},
		{TurnNote{PendingPrompt: "correction"}, "correction", false, false},
		{TurnNote{RoomHeld: true}, "correction", false, true},
		{TurnNote{Accepted: true}, "correction", true, false},
	} {
		step.note.Turn = 1
		if err := e.m.RunnerTurnNote(ctx, step.note); err != nil {
			t.Fatal(err)
		}
		turn, err := e.m.Store.ReadTurn(info.ID, 1)
		if err != nil || turn.HeldPrompt != step.prompt || turn.HeldAccepted != step.accepted || turn.RoomHeld != step.held {
			t.Fatalf("after %+v: prompt %q, accepted %v, held %v, error %v", step.note, turn.HeldPrompt, turn.HeldAccepted, turn.RoomHeld, err)
		}
	}
}

func TestCanceledHeldInputDoesNotChangeThePrompt(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
		turn.HeldPrompt, turn.HeldAccepted = "accepted", true
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.m.runnerWrites(info.ID, 1).waitSession(ctx, "never sent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled held input = %v", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.HeldPrompt != "accepted" || !turn.HeldAccepted || turn.RoomHeld {
		t.Fatalf("cancelled wait changed held input: %q accepted %v held %v: %v", turn.HeldPrompt, turn.HeldAccepted, turn.RoomHeld, err)
	}
}

func TestHeldNoteReplayDoesNotHoldAcceptedInputAgain(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if err := state.MigrateRequests(filepath.Dir(e.m.Store.root())); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	id := info.ID + "/1/held"
	request := func() context.Context { return state.ForRequest(callspec.WithRequest(actor, id), "runner/"+id) }
	note := TurnNote{Turn: 1, RoomHeld: true}
	if err := e.m.RunnerTurnNote(request(), note); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnNote(actor, TurnNote{Turn: 1, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnNote(request(), note); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed hold = %v, want ErrRepeated", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.RoomHeld || !turn.HeldAccepted {
		t.Fatalf("replayed hold changed accepted input: held %v accepted %v: %v", turn.RoomHeld, turn.HeldAccepted, err)
	}
}
