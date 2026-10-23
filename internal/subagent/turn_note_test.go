package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// Each note a runner sends records its one fact in the turn and its event
// line, through the same call the engine serves.
func TestTurnNotesRecordEachFact(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID})
	notes := []TurnNote{
		{Profile: &TurnProfile{Profile: "p", Model: "m", Harness: "h", Dialect: "d"}},
		{Dispatched: &DrainedPost{Steer: Steer{ID: "msg_live", TS: 7, Message: "live"}, Owed: "work:lead"}},
		{Delivered: &Steer{ID: "msg_live", TS: 7, Message: "live"}},
		{Delivery: &Delivery{TS: 1, Kind: "steer", Outcome: "deferred", WaitedMs: 5}},
		{Fallback: &harness.ModelFallback{From: "a", To: "b", Reason: "limit"}},
		{HarnessSession: "h-2"},
		{AccountSwitch: &harness.AccountSwitch{From: "x", To: "y"}},
		{CompactError: "too big"},
		{Repinned: "1.2.3"},
	}
	for _, n := range notes {
		n.Turn = 1
		if err := e.m.RunnerTurnNote(ctx, n); err != nil {
			t.Fatalf("note %+v: %v", n, err)
		}
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	live := slices.IndexFunc(turn.Steering, func(s Steer) bool { return s.ID == "msg_live" })
	switch {
	case turn.Profile != "p" || turn.Model != "m" || turn.Harness != "h" || turn.OutputDialect != "d":
		t.Fatalf("profile = %q %q %q %q", turn.Profile, turn.Model, turn.Harness, turn.OutputDialect)
	case live < 0 || !turn.Steering[live].Delivered || !slices.Contains(turn.ReportTo, "work:lead"):
		t.Fatalf("live post = %+v, owes %v; want it delivered and owed", turn.Steering, turn.ReportTo)
	case len(turn.Deliveries) == 0 || turn.Deliveries[len(turn.Deliveries)-1].Outcome != "deferred":
		t.Fatalf("deliveries = %+v", turn.Deliveries)
	case len(turn.Fallbacks) != 1 || turn.HarnessSession != "h-2" || turn.AccountSwitch == nil || turn.AccountSwitch.To != "y":
		t.Fatalf("fallbacks %+v, session %q, switch %+v", turn.Fallbacks, turn.HarnessSession, turn.AccountSwitch)
	}
	events, err := e.m.Store.Events(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[any]bool{}
	for _, ev := range events {
		seen[ev["t"]] = true
	}
	for _, kind := range []string{"steer_delivered", "defer", "model_fallback", "harness_session", "compact_failed", "runtime_repin"} {
		if !seen[kind] {
			t.Errorf("no %s event line", kind)
		}
	}
}

// A live post handed to the harness is appended once, even when the engine
// stops after the commit and the runner sends the note again with the same
// request ID.
func TestDispatchedNoteResentAfterACrashWritesOnce(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	if err := state.MigrateRequests(filepath.Dir(e.m.store().root())); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID})
	n := TurnNote{Turn: 1, Dispatched: &DrainedPost{Steer: Steer{ID: "msg_once", TS: 9, Message: "once"}}}
	if err := e.m.RunnerTurnNote(state.ForRequest(actor, "runner/1"), n); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnNote(state.ForRequest(actor, "runner/1"), n); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("resend = %v; want ErrRepeated, which the engine answers as done", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, s := range turn.Steering {
		if s.ID == "msg_once" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("msg_once appears %d times; want once", count)
	}
}

// A note with no fact, or with two, is refused and writes nothing: a note is
// one fact, never a turn patch.
func TestTurnNoteWithoutExactlyOneFactIsRefused(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID})
	before, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []TurnNote{
		{Turn: 1},
		{Turn: 1, HarnessSession: "h-2", Profile: &TurnProfile{Profile: "p"}},
		{Turn: 1, PendingPrompt: "pending", RoomHeld: true},
		{Turn: 1, Accepted: true, RoomHeld: true},
	} {
		if err := e.m.RunnerTurnNote(ctx, n); err == nil {
			t.Fatalf("note %+v was accepted; want a refusal", n)
		}
	}
	after, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil || after.HarnessSession != before.HarnessSession || after.Profile != before.Profile {
		t.Fatalf("a refused note wrote the turn: %q %q, %v", after.HarnessSession, after.Profile, err)
	}
}

// A turn's end is written once: a resend with the same request ID after the
// engine committed it writes nothing, and the record holds what the runner
// sent. A replacement whose startup timed out ends as that error.
func TestTurnEndResentAfterACrashWritesOnce(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	if err := state.MigrateRequests(filepath.Dir(e.m.store().root())); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID})
	cutoff := int64(42)
	end := TurnEnd{Turn: 1, Status: TurnOK, Result: []byte(`"the answer"`), InputCutoff: &cutoff, HarnessSession: "h-9", RawEnd: 7, NormEnd: 8}
	if err := e.m.RunnerTurnEnd(state.ForRequest(actor, "runner/end"), end); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnEnd(state.ForRequest(actor, "runner/end"), end); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("resend = %v; want ErrRepeated, which the engine answers as done", err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != TurnOK || turn.Result != "the answer" || turn.InputCutoff == nil || *turn.InputCutoff != 42 || turn.HarnessSession != "h-9" || turn.RawEnd != 7 {
		t.Fatalf("ended turn = %+v", turn)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(t *Turn) { t.StopReason = transferStartupTimeout }); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RunnerTurnEnd(actor, end); err != nil {
		t.Fatal(err)
	}
	if turn, err := e.m.Store.ReadTurn(info.ID, 1); err != nil || turn.Status != TurnError || turn.Error != transferStartupTimeout {
		t.Fatalf("timed-out replacement ended %q %q, %v; want the startup timeout error", turn.Status, turn.Error, err)
	}
}

// A turn whose end was written but whose record then cannot be read ends as
// the runner reported it. The failed read is not a write error, so the
// turn_end line is not marked as unwritten and the finish still runs.
func TestEndedTurnReadFailureIsNotAWriteError(t *testing.T) {
	status, msg := endedAs(func() (Turn, error) { return Turn{}, errors.New("disk read failed") }, TurnOK, "")
	if status != TurnOK || msg != "" {
		t.Fatalf("endedAs after a failed read = %q %q; want the runner's ok", status, msg)
	}
	status, msg = endedAs(func() (Turn, error) {
		return Turn{Status: TurnError, Error: transferStartupTimeout, Profile: "p"}, nil
	}, TurnOK, "")
	if status != TurnError || msg != transferStartupTimeout {
		t.Fatalf("endedAs = %q %q; want the record's", status, msg)
	}
}

// A runner's request ID starts with its session. The engine keys runner
// calls by that ID alone, so a call whose ID names another session is
// refused before it writes.
func TestRunnerCallRefusesARequestIDOfAnotherSession(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	actor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: info.ID})
	if err := e.m.RunnerSetPid(callspec.WithRequest(actor, "ses_other/1/2/3/4"), 4242); err == nil {
		t.Fatal("a request ID of another session was accepted")
	}
	if err := e.m.RunnerSetPid(callspec.WithRequest(actor, info.ID+"/1/2/3/4"), 4242); err != nil {
		t.Fatalf("the session's own request ID was refused: %v", err)
	}
}

// A runner's first spool creates the spool folder: the write it could not
// send is on disk, with its request ID, for the engine to apply.
func TestFirstSpoolCreatesTheFolder(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("RESPOND: done")
	e.wait(info.ID, 1)
	if err := e.m.spool(info.ID, []engineclient.Queued{{ID: info.ID + "/1", Method: "RunnerSetPid", Args: []any{7}}}); err != nil {
		t.Fatalf("first spool: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(sessionlife.RunnerSpoolDir(filepath.Dir(e.m.store().root())), info.ID+".jsonl"))
	if err != nil || !strings.Contains(string(raw), info.ID+"/1") {
		t.Fatalf("spool file = %q, %v; want the line with its request ID", raw, err)
	}
}
