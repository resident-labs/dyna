package engine

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func readySession(t *testing.T, s *Service, sid contract.SessionID) {
	t.Helper()
	err := s.store.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='ready',can_handoff=1 WHERE session=?", sid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func startOn(t *testing.T, s *Service, room contract.RoomID, name string, profile contract.ProfileName, host contract.HostName) contract.SessionID {
	t.Helper()
	res, e := s.SessionStart(t.Context(), contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: profile, Host: host})
	if e != nil {
		t.Fatal(e)
	}
	return res.Session
}

func TestHarnessSwitchPigToAgyAndBack(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "mover", "other", "hermeslab")
	readySession(t, s, sid)
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatalf("pig to agy: %v", e)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil || session.Profile != "gem" || !session.Handoff {
		t.Fatalf("session after the switch: %+v %v", session, err)
	}
	hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true})
	if problem != nil || !hello.Handoff || hello.Harness != contract.HarnessAgy {
		t.Fatalf("hello: %+v %v", hello, problem)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "other"}); e != nil {
		t.Fatalf("agy to pig: %v", e)
	}
	if session, err := s.store.Session(ctx, sid); err != nil || session.Profile != "other" || !session.Handoff {
		t.Fatalf("session after the switch back: %+v %v", session, err)
	}
}

func TestHarnessSwitchToAgyNeedsHermeslab(t *testing.T) {
	s, room, _, owner := agySetup(t)
	sid := startOn(t, s, room, "away", "other", "host")
	_, e := s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "runs only on host hermeslab") && !strings.Contains(e.Message, "agy runs only on host hermeslab") {
		t.Fatalf("err = %v", e)
	}
	if session, err := s.store.Session(t.Context(), sid); err != nil || session.Profile != "other" || session.Handoff {
		t.Fatalf("a refused switch changed the session: %+v %v", session, err)
	}
}

func TestHarnessSwitchHoldsTheAgyAccountLimit(t *testing.T) {
	s, room, _, owner := agySetup(t)
	startOn(t, s, room, "a1", "gem", "")
	startOn(t, s, room, "a2", "gem", "")
	sid := startOn(t, s, room, "third", "other", "hermeslab")
	readySession(t, s, sid)
	_, e := s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "allows 2 open sessions") {
		t.Fatalf("err = %v", e)
	}
}

// An agy turn that was running when the owner moved the session to Pi ends under agy's rules: one wake at its reset, no Pi ladder,
// no account rotation, and the handoff stays for the restart on Pi.
func TestRunningAgyTurnEndsUnderAgyAfterASwitchToPig(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "mid", "gem", "")
	readySession(t, s, sid)
	openTurn(t, s, ctx, room, sid, "t1")
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "other"}); e != nil {
		t.Fatal(e)
	}
	if _, p := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: "t1", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: 7}); p != nil {
		t.Fatal(p)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil || !session.Handoff {
		t.Fatalf("session: %+v %v", session, err)
	}
	var retries int
	var at int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT model_retries,model_retry_at FROM sessions WHERE id=?", sid).Scan(&retries, &at); err != nil || retries != 0 || at != s.now().Add(7*time.Second).UnixMilli() {
		t.Fatalf("retries %d at %d (want 0 at %d): %v", retries, at, s.now().Add(7*time.Second).UnixMilli(), err)
	}
}

func TestHarnessSwitchNeedsABoxThatCanHandOff(t *testing.T) {
	s, room, _, owner := agySetup(t)
	sid := startOn(t, s, room, "old", "other", "hermeslab")
	_, e := s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "cannot hand its conversation") {
		t.Fatalf("err = %v", e)
	}
}

// The box that is up when the session changes harness takes no new turn: the dispatch gate holds until a box has started since.
func TestSwitchHoldsDispatchUntilTheBoxRestarts(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "held", "other", "hermeslab")
	readySession(t, s, sid)
	if ok, err := s.store.CanDispatch(ctx, sid); err != nil || !ok {
		t.Fatalf("before the switch: %v %v", ok, err)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	if ok, err := s.store.CanDispatch(ctx, sid); err != nil || ok {
		t.Fatalf("the old box may take a turn: %v %v", ok, err)
	}
	if _, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true}); p != nil {
		t.Fatal(p)
	}
	if ok, err := s.store.CanDispatch(ctx, sid); err != nil || !ok {
		t.Fatalf("the restarted box may not: %v %v", ok, err)
	}
}

func TestSwitchRefusesAClosingSession(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "closing", "other", "hermeslab")
	readySession(t, s, sid)
	if err := s.store.MarkClosing(ctx, sid, false, s.now()); err != nil {
		t.Fatal(err)
	}
	_, p := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"})
	after, err := s.store.Session(ctx, sid)
	if p == nil || err != nil || after.Profile != "other" || after.Handoff {
		t.Fatalf("err=%v session=%+v read=%v", p, after, err)
	}
}

func TestHelloNamesEachHandoff(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "gen", "other", "hermeslab")
	readySession(t, s, sid)
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	hello := func() contract.BoxHelloResponse {
		res, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true})
		if p != nil {
			t.Fatal(p)
		}
		return res
	}
	first, again := hello(), hello()
	if !first.Handoff || first.HandoffGen == "" || first.HandoffGen != again.HandoffGen {
		t.Fatalf("a repeated hello changed the handoff: %q %q", first.HandoffGen, again.HandoffGen)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "other"}); e != nil {
		t.Fatal(e)
	}
	if next := hello(); next.HandoffGen == first.HandoffGen {
		t.Fatalf("a new handoff kept the old name %q", next.HandoffGen)
	}
}

// A turn that started on agy keeps the harness that served it when the session goes to Pi and back to agy while it runs: its
// quota reset is agy's one wake, not a step of Pi's ladder.
func TestRunningAgyTurnKeepsItsHarnessThroughASwitchThereAndBack(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "back", "gem", "")
	readySession(t, s, sid)
	openTurn(t, s, ctx, room, sid, "t1")
	for _, profile := range []contract.ProfileName{"other", "gem"} {
		if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: profile}); e != nil {
			t.Fatal(e)
		}
		readySession(t, s, sid)
	}
	if _, p := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: "t1", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: 7}); p != nil {
		t.Fatal(p)
	}
	var retries int
	var at int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT model_retries,model_retry_at FROM sessions WHERE id=?", sid).Scan(&retries, &at); err != nil || retries != 0 || at != s.now().Add(7*time.Second).UnixMilli() {
		t.Fatalf("retries %d at %d (want 0 at %d): %v", retries, at, s.now().Add(7*time.Second).UnixMilli(), err)
	}
}

// The fence is stored: a session whose box has not started since the switch is stale with no refresh mark, as after an engine
// restart, and is not once the box has said hello again.
func TestStoredFenceMakesTheBoxStale(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "fenced", "other", "hermeslab")
	readySession(t, s, sid)
	turns, stop := connectBox(t, s, ctx, sid, "same-agent")
	defer stop()
	_ = turns
	if s.Stale(sid) {
		t.Fatal("stale before the switch")
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	if h, ok := s.live.LookupSession(sid); ok {
		h.ClearRefresh()
	}
	if !s.Stale(sid) {
		t.Fatal("the box that has not started since the switch is not stale")
	}
	if _, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true}); p != nil {
		t.Fatal(p)
	}
	if s.Stale(sid) {
		t.Fatal("stale after the box started")
	}
}
