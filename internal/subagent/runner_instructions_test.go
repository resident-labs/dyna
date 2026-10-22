package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
)

func TestInstructionCreatorReadDoesNotRepairTheParent(t *testing.T) {
	e := newTestEnv(t)
	parent := completedFixtureSession(t, e)
	child := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateTurn(parent.ID, 1, func(turn *Turn) { turn.Status = TurnRunning }); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.UpdateMeta(child.ID, func(meta *Meta) { meta.ParentSession = parent.ID })
	if err != nil {
		t.Fatal(err)
	}
	_ = e.m.desiredInstructions(meta)
	if turn, err := e.m.Store.ReadTurn(parent.ID, 1); err != nil || turn.Status != TurnRunning {
		t.Fatalf("instruction creator read repaired its parent: status %s, error %v", turn.Status, err)
	}
}

func TestRunnerInstructionsSavesTheExactSnapshotAndCoveredNotes(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	note, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: "dyna", Kind: "instructions", To: []string{info.Address()}, Text: "old drift"})
	if err != nil {
		t.Fatal(err)
	}
	old := instructions.Build(instructions.Section{Name: "Old", Text: "old instructions"})
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
		meta.Instructions, meta.InstructionsStale, meta.InstructionsNotes = &old, true, []string{note.ID}
	}); err != nil {
		t.Fatal(err)
	}
	data := filepath.Dir(e.m.Store.root())
	if err := state.MigrateRequests(data); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	id := info.ID + "/1/instructions"
	request := func() context.Context { return state.ForRequest(callspec.WithRequest(actor, id), "runner/"+id) }
	first, err := e.m.RunnerInstructions(request(), 1)
	if err != nil || !first.Refreshed || first.Frozen.Hash == "" || !slices.Equal(first.Covered, []string{note.ID}) {
		t.Fatalf("frozen instructions = %+v, error %v", first, err)
	}
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	saved, done, err := state.For(db).RequestDone(t.Context(), "runner/"+id)
	if err != nil || !done || string(saved) != string(want) {
		t.Fatalf("reopened reply = %s, done %v, error %v; want %s", saved, done, err, want)
	}
	if _, err := e.m.goalStore().AddRule(info.RoomID, "owner", "", "A later change stays outside the snapshot."); err != nil {
		t.Fatal(err)
	}
	if err := e.m.noteInstructionsAt(e.ctx, info.ID, time.Now().Add(instructionQuietPeriod)); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || len(meta.InstructionsNotes) != 1 || meta.InstructionsNotes[0] == note.ID {
		t.Fatalf("later drift note = %v, error %v", meta.InstructionsNotes, err)
	}
	late := meta.InstructionsNotes[0]
	if _, err := e.m.RunnerInstructions(request(), 1); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed freeze = %v, want ErrRepeated", err)
	}
	meta, err = e.m.Store.ReadMeta(info.ID)
	if err != nil || meta.Instructions == nil || meta.Instructions.Hash != first.Frozen.Hash || !slices.Equal(meta.InstructionsNotes, []string{late}) {
		t.Fatalf("retry replaced the frozen snapshot or covered a later note: %+v, %v", meta, err)
	}
	claimed, err := e.m.RunnerClaimInput(actor, InputClaim{Turn: 1, Startup: true, Covered: first.Covered})
	if err != nil || len(claimed.Posts) != 1 || claimed.Posts[0].Steer.ID != late {
		t.Fatalf("snapshot input = %+v, error %v; want only later note %s", claimed, err, late)
	}
}

func TestRunnerInstructionsRetainsTheSnapshotAndUsesDriftNotes(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	frozen := e.m.desiredInstructions(info.Meta)
	if err := e.m.Store.writeTurn(info.ID, Turn{N: 2, Status: TurnQueued, Profile: "mock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.goalStore().AddRule(info.RoomID, "owner", "", "Retain the cached prefix."); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
		meta.Turns, meta.Instructions, meta.InstructionsChangedAt = 2, &frozen, time.Time{}
	}); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	got, err := e.m.RunnerInstructions(ctx, 2)
	if err != nil || got.Refreshed || got.Frozen.Hash != frozen.Hash || len(got.Covered) != 0 {
		t.Fatalf("retained instructions = %+v, error %v", got, err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || len(meta.InstructionsNotes) != 1 || meta.InstructionsKnown == nil || meta.InstructionsKnown.Hash == frozen.Hash {
		t.Fatalf("drift metadata = %+v, error %v", meta, err)
	}
	note, err := e.m.rooms().Message(info.RoomID, meta.InstructionsNotes[0])
	if err != nil || note.Wake || note.Kind != "instructions" || !strings.Contains(note.Text, "Retain the cached prefix.") {
		t.Fatalf("drift note = %+v, error %v", note, err)
	}
}

func TestRunnerInstructionsRollbackKeepsTheSnapshotAndReceiptUntouched(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	data := filepath.Dir(e.m.Store.root())
	if err := state.MigrateRequests(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_instruction_snapshot BEFORE UPDATE ON session_records
BEGIN SELECT RAISE(ABORT, 'reject instruction snapshot'); END`); err != nil {
		t.Fatal(err)
	}
	events := &eventLog{}
	state.For(db).SetPublish(events.add)
	actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	id := info.ID + "/1/freeze"
	ctx := state.ForRequest(callspec.WithRequest(actor, id), "runner/"+id)
	got, err := e.m.RunnerInstructions(ctx, 1)
	if err == nil || got.Frozen.Hash != "" || len(got.Covered) != 0 {
		t.Fatalf("failed snapshot returned a result: %+v, error %v", got, err)
	}
	meta, metaErr := e.m.Store.ReadMeta(info.ID)
	_, done, receiptErr := state.For(db).RequestDone(t.Context(), "runner/"+id)
	published := events.count("sessions", info.ID)
	if metaErr != nil || meta.Instructions != nil || done || receiptErr != nil || published != 0 {
		t.Fatalf("rollback changed snapshot/receipt/events: snapshot %v, done %v, events %d, errors %v %v", meta.Instructions, done, published, metaErr, receiptErr)
	}
	if _, err := db.Exec("DROP TRIGGER reject_instruction_snapshot"); err != nil {
		t.Fatal(err)
	}
	got, err = e.m.RunnerInstructions(ctx, 1)
	published = events.count("sessions", info.ID)
	if err != nil || got.Frozen.Hash == "" || !got.Refreshed || published != 1 {
		t.Fatalf("retry snapshot = %+v, events %d, error %v", got, published, err)
	}
	replay := state.ForRequest(callspec.WithRequest(actor, id), "runner/"+id)
	if _, err := e.m.RunnerInstructions(replay, 1); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed snapshot = %v; want ErrRepeated", err)
	}
	if got := events.count("sessions", info.ID); got != 1 || events.members(info.ID) != 0 {
		t.Fatalf("replay events: sessions %d, members %d; want one internal event only", got, events.members(info.ID))
	}
}

func TestRunnerUsesTheEngineInstructionReplyWithoutALocalWrite(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	want := FrozenInstructions{Frozen: instructions.Build(instructions.Section{Name: "Engine", Text: "engine-owned snapshot"}), Covered: []string{"msg_covered"}, Refreshed: true}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/RunnerInstructions" || !strings.HasPrefix(r.Header.Get(callspec.RequestHeader), info.ID+"/") {
			t.Errorf("wrong instruction call: path %s, request %s", r.URL.Path, r.Header.Get(callspec.RequestHeader))
		}
		var turns []int
		if err := json.NewDecoder(r.Body).Decode(&turns); err != nil || !slices.Equal(turns, []int{1}) {
			t.Errorf("instruction args %v, error %v", turns, err)
		}
		_ = json.NewEncoder(w).Encode(want)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	e.m.Engine = engineclient.New(path)
	writes := e.m.runnerWrites(info.ID, 1)
	t.Cleanup(func() { writes.close(context.Background()) })
	got, err := writes.instructions(t.Context())
	if err != nil || got.Frozen.Hash != want.Frozen.Hash || !got.Refreshed || !slices.Equal(got.Covered, want.Covered) {
		t.Fatalf("runner instruction reply = %+v, error %v", got, err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || meta.Instructions != nil {
		t.Fatalf("the runner wrote its own snapshot: %+v, error %v", meta.Instructions, err)
	}
}

func TestRunnerInstructionsRefusesAnotherTurnAndClosedSessions(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.RunnerInstructions(t.Context(), 1); err == nil {
		t.Fatal("an unidentified caller received instructions")
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
	for _, turn := range []int{0, 2} {
		if _, err := e.m.RunnerInstructions(ctx, turn); err == nil {
			t.Fatalf("instructions accepted turn %d", turn)
		}
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Closed = time.Now() }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RunnerInstructions(ctx, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed session instructions = %v, want ErrClosed", err)
	}
}
