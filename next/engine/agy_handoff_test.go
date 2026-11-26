package engine

import (
	"context"
	"database/sql"
	"errors"
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
	if err != nil || session.Profile != "gem" || !session.Handoff || session.AgyAccount != "" {
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

type accountList struct {
	names []string
	err   error
}

func (a accountList) list(context.Context, contract.HostName) ([]string, error) {
	return a.names, a.err
}

func agyLimit(t *testing.T, s *Service, room contract.RoomID, sid contract.SessionID, turn contract.TurnID, reset int64, between ...func()) {
	t.Helper()
	ctx := t.Context()
	openTurn(t, s, ctx, room, sid, turn)
	for _, f := range between {
		f()
	}
	if _, p := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: reset}); p != nil {
		t.Fatal(p)
	}
}

func TestAgyLimitMovesToAnotherAccountOnce(t *testing.T) {
	s, room, _, _ := agySetup(t)
	sid := startOn(t, s, room, "limited", "gem", "")
	readySession(t, s, sid)
	s.SetAgyAccounts(accountList{names: []string{"main", "work", "../bad"}}.list)
	agyLimit(t, s, room, sid, "t1", 3600)
	session, err := s.store.Session(t.Context(), sid)
	if err != nil || session.AgyAccount != "work" || !session.Handoff {
		t.Fatalf("after the first limit: %+v %v", session, err)
	}
	if handle, ok := s.live.LookupSession(sid); !ok || handle.Refresh() == "" {
		t.Fatal("the box was not marked to restart on the new account")
	}
	// The new account is limited too. The wake that follows a move is not an owner post, so the first account is still in the
	// streak (the test's own wake is an owner post, which ends it, so it is put back): nothing is left, and it waits for the earlier reset, which is main's.
	agyLimit(t, s, room, sid, "t2", 7200, func() {
		err := s.store.Write(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec("INSERT OR REPLACE INTO agy_streak(session,account,reset_at) VALUES(?,'main',?)", sid, s.now().Add(time.Hour).UnixMilli())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	session, err = s.store.Session(t.Context(), sid)
	if err != nil || session.AgyAccount != "main" {
		t.Fatalf("after the second limit: %+v %v", session, err)
	}
}

func TestAgyLimitStaysWhenNoAccountIsFree(t *testing.T) {
	for name, list := range map[string]accountList{
		"one account":   {names: []string{"main"}},
		"listing fails": {err: errors.New("host down")},
	} {
		t.Run(name, func(t *testing.T) {
			s, room, _, _ := agySetup(t)
			sid := startOn(t, s, room, "limited", "gem", "")
			readySession(t, s, sid)
			s.SetAgyAccounts(list.list)
			agyLimit(t, s, room, sid, "t1", 3600)
			if session, err := s.store.Session(t.Context(), sid); err != nil || session.AgyAccount != "" || session.Handoff {
				t.Fatalf("session: %+v %v", session, err)
			}
		})
	}
}

func TestAgyFailureWithoutAResetNeverMoves(t *testing.T) {
	s, room, _, _ := agySetup(t)
	sid := startOn(t, s, room, "denied", "gem", "")
	readySession(t, s, sid)
	s.SetAgyAccounts(accountList{names: []string{"main", "work"}}.list)
	ctx := t.Context()
	openTurn(t, s, ctx, room, sid, "t1")
	if _, p := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: "t1", Outcome: contract.TurnFailed, Cause: contract.CauseHarnessFailed, Error: "403 PERMISSION_DENIED"}); p != nil {
		t.Fatal(p)
	}
	if session, err := s.store.Session(ctx, sid); err != nil || session.AgyAccount != "" {
		t.Fatalf("session: %+v %v", session, err)
	}
}

func TestAgySessionContextNamesItsAccount(t *testing.T) {
	s, room, _, _ := agySetup(t)
	sid := startOn(t, s, room, "ctx", "gem", "")
	res, problem := s.ContextSnapshot(t.Context(), boxCaller(sid), contract.ContextSnapshotRequest{Section: contract.ContextProfiles})
	if problem != nil {
		t.Fatal(problem)
	}
	var text string
	for _, sec := range res.Sections {
		if sec.Key == contract.ContextProfiles {
			text = sec.Text
		}
	}
	if !strings.Contains(text, "you run on agy account main;") {
		t.Fatalf("profiles section %q", text)
	}
}

// An agy turn that was running when the owner moved the session to Pi ends under agy's rules: one wake at its reset, no Pi ladder,
// no account rotation, and the handoff stays for the restart on Pi.
func TestRunningAgyTurnEndsUnderAgyAfterASwitchToPig(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "mid", "gem", "")
	readySession(t, s, sid)
	s.SetAgyAccounts(accountList{names: []string{"main", "work"}}.list)
	openTurn(t, s, ctx, room, sid, "t1")
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "other"}); e != nil {
		t.Fatal(e)
	}
	if _, p := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: "t1", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: 7}); p != nil {
		t.Fatal(p)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil || session.AgyAccount != "" || !session.Handoff {
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

func TestAgyLimitNeverMovesFromABoxThatCannotHandOff(t *testing.T) {
	s, room, _, _ := agySetup(t)
	sid := startOn(t, s, room, "old", "gem", "")
	readySession(t, s, sid)
	err := s.store.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET can_handoff=0 WHERE session=?", sid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	s.SetAgyAccounts(func(context.Context, contract.HostName) ([]string, error) {
		t.Error("listed accounts for a box that cannot hand off")
		return []string{"main", "work"}, nil
	})
	agyLimit(t, s, room, sid, "t1", 3600)
	if session, err := s.store.Session(t.Context(), sid); err != nil || session.AgyAccount != "" || session.Handoff {
		t.Fatalf("session: %+v %v", session, err)
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

// The account move holds dispatch the same way, until the box has restarted on the new account.
func TestMoveHoldsDispatchUntilTheBoxRestarts(t *testing.T) {
	s, room, _, _ := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "moving", "gem", "")
	readySession(t, s, sid)
	s.SetAgyAccounts(accountList{names: []string{"main", "work"}}.list)
	agyLimit(t, s, room, sid, "t1", 3600)
	if ok, err := s.store.CanDispatch(ctx, sid); err != nil || ok {
		t.Fatalf("the old account's box may take a turn: %v %v", ok, err)
	}
	if _, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true}); p != nil {
		t.Fatal(p)
	}
	if ok, err := s.store.CanDispatch(ctx, sid); err != nil || !ok {
		t.Fatalf("after the restart: %v %v", ok, err)
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
