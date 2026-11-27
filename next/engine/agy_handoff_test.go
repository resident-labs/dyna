package engine

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func readySession(t *testing.T, s *Service, sid contract.SessionID) {
	t.Helper()
	err := s.store.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='ready',can_handoff=1,harness=(CASE WHEN (SELECT profile FROM sessions WHERE id=boxes.session) IN ('gem','gem2') THEN 'agy' ELSE 'pig' END) WHERE session=?", sid)
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
	hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true, LaunchGen: launchGen(t, s, sid)})
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
	if _, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true, LaunchGen: launchGen(t, s, sid)}); p != nil {
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
		res, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true, LaunchGen: launchGen(t, s, sid)})
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
	if _, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true, LaunchGen: launchGen(t, s, sid)}); p != nil {
		t.Fatal(p)
	}
	if s.Stale(sid) {
		t.Fatal("stale after the box started")
	}
}

// launchGen is the handoff generation a container made now for the session would carry, which the box reports in its hello.
func launchGen(t *testing.T, s *Service, sid contract.SessionID) int64 {
	t.Helper()
	session, err := s.store.Session(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	return session.HandoffGen
}

// A box made before the switch says hello late, with the old launch generation: the fence holds, so it takes no turn.
func TestLateHelloFromTheOldContainerKeepsTheFence(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "late", "other", "hermeslab")
	readySession(t, s, sid)
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	if _, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true}); p != nil {
		t.Fatal(p)
	}
	if ok, err := s.store.CanDispatch(ctx, sid); err != nil || ok {
		t.Fatalf("the old container may take a turn: %v %v", ok, err)
	}
	if !s.Stale(sid) {
		t.Fatal("the old container is not stale")
	}
}

// The harness of a turn is the one of the box it was dispatched on, so removing the profile it ran on while it ends, after the
// session moved to Pi, does not send its quota reset into Pi's ladder.
func TestRunningAgyTurnKeepsItsHarnessWhenItsProfileIsRemoved(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "gone", "gem", "")
	readySession(t, s, sid)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET harness='agy' WHERE session=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	openTurn(t, s, ctx, room, sid, "t1")
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "other"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "gem"}); e != nil {
		t.Fatal(e)
	}
	if _, p := s.TurnOutput(ctx, boxCaller(sid), contract.TurnOutputRequest{Turn: "t1", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: 7}); p != nil {
		t.Fatal(p)
	}
	var retries int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT model_retries FROM sessions WHERE id=?", sid).Scan(&retries); err != nil || retries != 0 {
		t.Fatalf("retries %d: %v", retries, err)
	}
}

// A box still starting that was made before the switch is refused its hello, so it gets no initialize of the new harness and no
// startup time, and its stop is requested for a replacement made with the new layout.
func TestStartingBoxMadeBeforeTheSwitchIsRefusedAndStopped(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "old-layout", "other", "hermeslab")
	readySession(t, s, sid)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='starting' WHERE session=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	hello, p := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true, WaitForChannel: true})
	if p == nil || p.Code != contract.ErrConflict || hello.Agy != nil {
		t.Fatalf("hello = %+v, %v", hello, p)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT want FROM boxes WHERE id=?", box.ID).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if want != "stop" {
		t.Fatalf("box want %q", want)
	}
}

// A profile edit takes the registry's write lock for its check, so it waits for an assignment that holds the read lock from its
// decision to its commit; the assignment cannot decide on a definition the edit changes before it commits.
func TestProfileEditWaitsForAnAssignmentInFlight(t *testing.T) {
	config, err := loadAgyProfiles(t, agyProfilesJSON)
	if err != nil {
		t.Fatal(err)
	}
	inside, release, done := make(chan struct{}), make(chan struct{}), make(chan *contract.Error)
	go config.WithProfile("test", func(func(contract.ProfileName) contract.Harness) *contract.Error {
		close(inside)
		<-release
		return nil
	})
	<-inside
	go func() {
		done <- config.Set("test", &profile{Provider: "google", Model: "gemini-3.8-flash", AllowedModels: []string{"gemini-3.8-flash"}, Thinking: "low", MaxConcurrent: 1, Harness: contract.HarnessAgy}, false, func(profile) *contract.Error { return nil })
	}()
	select {
	case <-done:
		t.Fatal("the edit committed while an assignment was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if problem := <-done; problem != nil {
		t.Fatal(problem)
	}
	if config.Harness("test") != contract.HarnessAgy {
		t.Fatal("the edit did not apply")
	}
}

// A profile edit queued behind an assignment must not hang it: the assignment reads harnesses through the lock it holds, and a second
// read lock would block behind the queued writer, which waits for the assignment.
func TestHarnessReadUnderTheHeldLockCompletesWithAnEditQueued(t *testing.T) {
	config, err := loadAgyProfiles(t, agyProfilesJSON)
	if err != nil {
		t.Fatal(err)
	}
	finished, edited := make(chan contract.Harness, 1), make(chan struct{})
	go func() {
		config.WithProfile("gem", func(harnessOf func(contract.ProfileName) contract.Harness) *contract.Error {
			queued := make(chan struct{})
			go func() {
				close(queued)
				config.Set("other", &profile{Provider: "openai", Model: "m", AllowedModels: []string{"m"}, Thinking: "low", Harness: contract.HarnessPig}, false, func(profile) *contract.Error { return nil })
				close(edited)
			}()
			<-queued
			time.Sleep(100 * time.Millisecond)
			finished <- harnessOf("other")
			return nil
		})
	}()
	select {
	case h := <-finished:
		if h != contract.HarnessPig {
			t.Fatalf("harness %q", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: a harness read under the held lock blocked behind a queued edit")
	}
	// The edit writes the profile file in the test's folder, so it must be done before the folder goes.
	<-edited
}

// The launch generation is compared in the write that would extend the startup, so a switch that commits after the hello read the
// session still stops the old container instead of giving it startup time.
func TestStartupWriteComparesTheLaunchGeneration(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	sid := startOn(t, s, room, "race", "other", "hermeslab")
	readySession(t, s, sid)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='starting' WHERE session=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	// The hello read the session above; the switch commits now.
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	current, err := s.store.Box(ctx, before.Box)
	if err != nil {
		t.Fatal(err)
	}
	event, err := s.engineEvent(before.Room, contract.BoxChanged{})
	if err != nil {
		t.Fatal(err)
	}
	obsolete, err := s.store.BeginSessionStartup(ctx, current, before.HandoffGen, event)
	if err != nil || !obsolete {
		t.Fatalf("obsolete=%v err=%v", obsolete, err)
	}
	var want string
	var due int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT want,due_at FROM boxes WHERE id=?", current.ID).Scan(&want, &due); err != nil {
		t.Fatal(err)
	}
	if want != "stop" || due >= event.Time.Add(box.SessionStartupBudget).UnixMilli() {
		t.Fatalf("want %q due %d: the obsolete launch was given startup time", want, due)
	}
}
