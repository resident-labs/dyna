package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

// testEnv is an isolated data dir plus a manager that runs turns in-process.
type testEnv struct {
	t    *testing.T
	m    *Manager
	ctx  context.Context
	room room.Record
}

func newTestEnv(t *testing.T, profiles ...profile.Profile) *testEnv {
	t.Helper()
	storetest.Isolate(t)
	if len(profiles) == 0 {
		profiles = []profile.Profile{mockProfile("mock")}
	}
	store := &profile.Store{Profiles: profiles}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Close cleanup runs on the manager's workers. Bind them to this test so
	// they stop before the test restores the process environment.
	workers := NewWorkers(ctx, nil)
	t.Cleanup(func() {
		cancel()
		if left := workers.Wait(time.Second); len(left) > 0 {
			t.Errorf("session workers still running: %v", left)
		}
	})
	m := &Manager{
		Store:    DefaultStore(),
		Profiles: func() (*profile.Store, error) { return store, nil },
		Settings: settings.Defaults,
		Spawner:  InProcessSpawner{Ctx: ctx},
		Workers:  workers,
	}
	// Real Dyna sessions are always stamped with a room; tests that simulate
	// one inherit the room "work".
	rec, err := m.rooms().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvRoomID, rec.ID)
	t.Setenv(mode.EnvRoom, rec.Name)
	e := &testEnv{t: t, m: m, ctx: ctx, room: rec}
	e.joinRunners()
	return e
}

// joinRunners waits for all retirement work, which runs after turn.lock is
// released. Register it after temporary store setup so cleanup cannot race it.
func (e *testEnv) joinRunners() func() {
	ctx, cancel := context.WithCancel(e.ctx)
	spawner := &joinedFixtureSpawner{ctx: ctx}
	e.m.Spawner = spawner
	e.t.Cleanup(func() { cancel(); spawner.runners.Wait() })
	return spawner.runners.Wait
}

type joinedFixtureSpawner struct {
	ctx     context.Context
	runners sync.WaitGroup
	turns   sync.Map // fixtureTurn identifies retirement after the turn lock is released.
}

type fixtureTurn struct {
	id string
	n  int
}

func (s *joinedFixtureSpawner) Spawn(m *Manager, id string, n int, lock *os.File) error {
	done := make(chan struct{})
	s.turns.Store(fixtureTurn{id, n}, done)
	s.runners.Add(1)
	go func() {
		defer s.runners.Done()
		defer close(done)
		_ = m.RunTurn(s.ctx, id, n, lock)
	}()
	return nil
}

func mockProfile(name string) profile.Profile {
	return profile.Profile{Name: name, Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true}
}

func (e *testEnv) start(task string, opts ...func(*StartOptions)) (Info, Turn) {
	e.t.Helper()
	so := StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: e.t.TempDir(), Task: task}
	for _, fn := range opts {
		fn(&so)
	}
	info, turn, err := e.m.Start(context.Background(), so)
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	return info, turn
}

// completedFixtureSession seeds the prior completed turn. Tests of later
// lifecycle transitions still run the real follow-up, without an unrelated
// initial harness call and its instruction, journal, and retirement writes.
func completedFixtureSession(t *testing.T, e *testEnv, prompts ...string) Info {
	t.Helper()
	id := NewID()
	prompt := "RESPOND: initial"
	if len(prompts) > 0 {
		prompt = prompts[0]
	}
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	meta := Meta{ID: id, Name: "fixture-" + id, RoomID: e.room.ID, Room: e.room.Name,
		Profile: "mock", Dir: t.TempDir(), Depth: 1, Created: now, LastActivity: now,
		Turns: 1, Resumable: true, HarnessSession: "mock-" + id}
	if err := e.m.Store.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnOK, Profile: "mock",
		Prompt: prompt, HarnessSession: meta.HarnessSession,
		Created: now, Started: now, Ended: now}); err != nil {
		t.Fatal(err)
	}
	info, err := e.m.Store.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// doneGoals finishes every open goal, as a worker does before it finishes.
func (e *testEnv) doneGoals() {
	e.t.Helper()
	st, err := e.m.goalStore().Read()
	if err != nil {
		e.t.Fatal(err)
	}
	for room, r := range st.Rooms {
		for id, g := range r.Goals {
			if g.Open() {
				if _, err := e.m.goalStore().Finish(room, id, "owner", "done", false); err != nil {
					e.t.Fatal(err)
				}
			}
		}
	}
}

// doneGoal finishes the goal info holds, as a worker does before it finishes.
func (e *testEnv) doneGoal(info Info) {
	e.t.Helper()
	held, err := e.m.goalStore().OpenHeldBy(info.RoomID, info.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, g := range held {
		if _, err := e.m.goalStore().Finish(info.RoomID, g.ID, "owner", "done", false); err != nil {
			e.t.Fatal(err)
		}
	}
}

// finish makes ref dormant the way a turn end does: it finishes the session's
// goals, then runs one more turn that leaves no work.
func (e *testEnv) finish(ref string) (Info, error) {
	e.doneGoals()
	if _, err := e.m.Assign(e.ctx, ref, "RESPOND: nothing left", "", time.Time{}); err != nil {
		return Info{}, err
	}
	info, err := e.m.Store.Info(ref)
	if err != nil {
		return info, err
	}
	e.wait(ref, info.Turns)
	return e.m.Store.Info(ref)
}

func (e *testEnv) wait(ref string, turn int) (Info, Turn) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	if current, err := e.m.Store.Resolve(ref); err == nil && turn > current.Turns {
		if current.Status == StatusRunning || current.Status == StatusQueued {
			if _, _, err := e.waitTurn(ctx, ref, current.Turns); err != nil {
				e.t.Fatal(err)
			}
		}
		if _, err := e.m.TickSessions(ctx); err != nil {
			e.t.Fatal(err)
		}
	}
	info, t, err := e.waitTurn(ctx, ref, turn)
	if err != nil {
		e.t.Fatalf("Wait(%s, %d): %v", ref, turn, err)
	}
	return info, t
}

// waitTurn joins fixture runners through their completion event. Fixtures with
// other spawners retain the public waiter's completion and repair checks.
func (e *testEnv) waitTurn(ctx context.Context, ref string, turn int) (Info, Turn, error) {
	store := e.m.Store
	info, err := store.Resolve(ref)
	if err != nil {
		return Info{}, Turn{}, err
	}
	if turn == 0 {
		turn = info.Turns
	}
	if turn <= 0 || turn > info.Turns {
		return info, Turn{}, fmt.Errorf("session %s has no turn %d (it has %d)", info.ID, turn, info.Turns)
	}
	// Wait for the fixture goroutine to return before changing fixture
	// state or removing its temporary store.
	if spawner, ok := e.m.Spawner.(*joinedFixtureSpawner); ok {
		if done, found := spawner.turns.Load(fixtureTurn{info.ID, turn}); found {
			select {
			case <-done.(chan struct{}):
			case <-ctx.Done():
				return info, Turn{}, ctx.Err()
			}
		}
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := store.ReadTurn(info.ID, turn)
		if err != nil {
			return info, Turn{}, err
		}
		if current.Done() && !store.lockHeld(info.ID) {
			info, _ = store.Info(info.ID)
			return info, current, nil
		}
		if !current.Done() {
			_, _ = store.Info(info.ID)
		}
		select {
		case <-ctx.Done():
			return info, current, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *testEnv) waitStatus(ref, want string) Info {
	e.t.Helper()
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		e.t.Fatal(err)
	}
	// Every session record write publishes its committed change, so the
	// waiter wakes on the store's own signal instead of a clock sample.
	changed := make(chan struct{}, 1)
	state.For(db).SetPublish(func(ev hub.Event) {
		if ev.Topic == sessionlife.Topic {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	})
	e.t.Cleanup(func() { state.For(db).SetPublish(nil) })
	deadline := time.After(10 * time.Second)
	for {
		info, err := e.m.Store.Resolve(ref)
		if err != nil {
			e.t.Fatalf("session %s: %v", ref, err)
		}
		if info.Status == want {
			return info
		}
		if info.Meta.Status == want {
			// The record already has the wanted status, but a finishing turn
			// still holds the turn lock and Info then reports running. Waiting
			// for that lock is the signal; its release publishes no change.
			e.awaitTurnLock(info.ID)
			continue
		}
		select {
		case <-changed:
		case <-deadline:
			e.t.Fatalf("session %s never became %s (now %s, err %v)", ref, want, info.Status, err)
		}
	}
}

// awaitTurnLock returns once no turn holds the session's turn lock.
func (e *testEnv) awaitTurnLock(id string) {
	e.t.Helper()
	path, err := e.m.Store.path(id, "turn.lock")
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unlock, err := store.LockContext(ctx, path)
	if err != nil {
		e.t.Fatalf("session %s turn lock: %v", id, err)
	}
	unlock()
}

// send stores an owner post, then uses the shared wake path the engine calls.
func (e *testEnv) send(ref, text string, opts WakeOptions) (WakeResult, error) {
	e.t.Helper()
	info, err := e.m.Store.Resolve(ref)
	if err != nil {
		return WakeResult{}, err
	}
	msg, err := e.m.RoomStore().Post(info.RoomID, room.PostInput{Wake: true,
		From: info.Room, To: []string{info.Address()}, Via: []string{"@" + info.Address()}, Text: text,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	opts.MessageID = msg.ID
	return e.m.Wake(e.ctx, ref, opts)
}

func TestStartWaitOutput(t *testing.T) {
	e := newTestEnv(t)
	info, turn := e.start("SLEEP:1 RESPOND: hello from the subagent")
	if !strings.HasPrefix(info.ID, "ses_") || turn.N != 1 || info.Depth != 1 || !info.Resumable {
		t.Fatalf("started %#v turn %#v", info, turn)
	}
	// Keep coverage of the public waiter while other fixtures use fast polling.
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	got, done, err := e.m.Wait(ctx, info.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != TurnOK || done.Result != "hello from the subagent" || got.Status != StatusIdle || got.Turns != 1 {
		t.Fatalf("finished %#v / %#v", got, done)
	}
	if done.HarnessSession == "" || got.HarnessSession != done.HarnessSession || got.Usage.Total() == 0 {
		t.Fatalf("session/usage not recorded: %#v / %#v", got, done)
	}
	if done.Started.IsZero() || done.Ended.IsZero() {
		t.Fatalf("turn times missing: %#v", done)
	}
	// Sessions keep no work journal: progress lives in the transcript and in
	// room posts.
	dir, _ := e.m.Store.Dir(info.ID)
	if _, err := os.Stat(filepath.Join(dir, "agents", "1", "journal.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("session journal exists: %v", err)
	}
}

func TestSendWhileIdleResumesTheSameHarnessSession(t *testing.T) {
	e := newTestEnv(t)
	// Initial harness creation has separate coverage. This test needs only
	// an idle session and the real follow-up that resumes its saved identity.
	info := completedFixtureSession(t, e)
	res, err := e.send(info.ID, "SLEEP:1 RESPOND: second", WakeOptions{})
	if err != nil || res.Steered || res.Turn != 2 {
		t.Fatalf("Send = %#v, %v; want a follow-up turn 2", res, err)
	}
	got, second := e.wait(info.ID, 2)
	if second.Result != "second" || second.HarnessSession != info.HarnessSession || got.HarnessSession != info.HarnessSession {
		t.Fatalf("follow-up did not resume the same harness session: first=%q second=%q meta=%q", info.HarnessSession, second.HarnessSession, got.HarnessSession)
	}
	if got.Turns != 2 {
		t.Fatalf("turns = %d", got.Turns)
	}
}

func TestSendWhileRunningIsDeliveredAsSteering(t *testing.T) {
	e := newTestEnv(t)
	ready, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	e.m.checkpoint = func(stage string) {
		if stage == "turn-started" {
			close(ready)
			select {
			case <-release:
			case <-e.ctx.Done():
			}
		}
	}
	joined := e.joinRunners()
	info, _ := e.start("long job SLEEP:0")
	<-ready
	res, err := e.send(info.ID, "prioritize the parser", WakeOptions{})
	if err != nil || !res.Steered || res.Turn != 1 {
		t.Fatalf("Send = %#v, %v; want steering of turn 1", res, err)
	}
	unblock()
	joined()
	done, err := e.m.Store.ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r := resultString(done); !strings.Contains(r, "prioritize the parser") {
		t.Fatalf("turn result = %#v", done.Result)
	}
	if len(done.Steering) != 1 || !strings.Contains(done.Steering[0].Message, "prioritize the parser") || done.Steering[0].ID == "" || !done.Steering[0].Delivered {
		t.Fatalf("steering record = %#v", done.Steering)
	}
}

// Hold startup before the initial drain. A post here must
// reach the prompt once, and Running must not be visible before the live source
// takes over. No sleep is used to decide when the post arrives.
func TestStartupPostDrainsOnceBeforeRunning(t *testing.T) {
	e := newTestEnv(t)
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	e.m.Settings = func() settings.Values {
		all, _ := e.m.Store.List(false)
		for _, s := range all {
			if s.Pid == os.Getpid() {
				once.Do(func() {
					close(ready)
					select {
					case <-release:
					case <-e.ctx.Done():
					}
				})
				break
			}
		}
		return settings.Defaults()
	}
	info, _ := e.start("RESPOND: startup SLEEP:50")
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("startup barrier not reached")
	}
	before, err := e.m.Store.Info(info.ID)
	if err != nil || before.Status != StatusQueued {
		t.Fatalf("startup state = %+v, %v; want queued until the live source is ready", before, err)
	}
	sent, err := e.send(info.ID, "SLEEP:1 startup post reaches the parser", WakeOptions{})
	if err != nil || !sent.Steered || !sent.Queued || sent.Turn != 1 {
		t.Fatalf("startup wake: %+v %v", sent, err)
	}
	close(release)
	_, done := e.wait(info.ID, 1)
	if len(done.Steering) != 1 || !done.Steering[0].Delivered || done.Steering[0].ID == "" || strings.Count(resultString(done), "startup post reaches the parser") != 1 {
		t.Fatalf("startup post must reach the prompt once: %+v", done)
	}
	if strings.Contains(resultString(done), "steered:") {
		t.Fatalf("post arrived before the initial drain, but was delivered live: %+v", done)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: follow-up", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, followup := e.wait(info.ID, 2)
	if len(followup.Steering) != 1 || strings.Contains(followup.Prompt, "startup post reaches the parser") {
		t.Fatalf("startup post redelivered: %+v", followup)
	}
}

func TestDrainUndeliveredRoomMessageOnNextTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: first")
	e.wait(info.ID, 1)
	current, err := e.m.Store.Info(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RoomStore().Post(current.RoomID, room.PostInput{
		From: current.Room, To: []string{current.Address()}, Via: []string{"@" + current.Address()}, Text: "lost direct message",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: second", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, second := e.wait(info.ID, 2)
	if len(second.Steering) != 2 || !strings.Contains(second.Steering[0].Message, "lost direct message") || !second.Steering[0].Delivered || !strings.Contains(second.Prompt, "lost direct message") || !strings.Contains(second.Prompt, "RESPOND: second") {
		t.Fatalf("drained steering = %#v", second.Steering)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: third", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, third := e.wait(info.ID, 3)
	if len(third.Steering) != 1 || strings.Contains(third.Prompt, "lost direct message") {
		t.Fatalf("third turn redelivered drained message: %#v", third)
	}
}

func TestOldUnnamedSessionHasAddressableFallbackName(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ok", func(o *StartOptions) { o.Name = "named" })
	e.wait(info.ID, 1)
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Name = "" }); err != nil {
		t.Fatal(err)
	}
	if err := sessionlife.Migrate(filepath.Dir(e.m.Store.root())); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "work:agent-" + info.ID[len(info.ID)-8:]
	if meta.Address() != want {
		t.Fatalf("fallback address = %q, want %q", meta.Address(), want)
	}
	got, err := e.m.Store.Resolve(want)
	if err != nil || got.ID != info.ID {
		t.Fatalf("Resolve(%q) = %#v, %v", want, got, err)
	}
}

func resultString(t Turn) string {
	s, _ := t.Result.(string)
	return s
}

// A post longer than the steering limit steers as a bounded frame that
// points to the stored post.
func TestLongPostSteersAsPointerFrame(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("job SLEEP:20000")
	e.waitStatus(info.ID, StatusRunning)
	res, err := e.send(info.ID, strings.Repeat("x", 2500), WakeOptions{})
	if err != nil || !res.Steered {
		t.Fatalf("Wake = %#v, %v", res, err)
	}
	_, done := e.wait(info.ID, 1)
	if len(done.Steering) != 1 || !done.Steering[0].Delivered || done.Steering[0].ID == "" || len(done.Steering[0].Message) > runstore.MaxSteeringMessageBytes || !strings.Contains(done.Steering[0].Message, "Full text: dyna room read") {
		t.Fatalf("steering = %#v", done.Steering)
	}
}

func TestStopCancelsTurnAndKeepsSessionResumable(t *testing.T) {
	e := newTestEnv(t)
	db, err := database.Open(filepath.Dir(e.m.Store.root()))
	if err != nil {
		t.Fatal(err)
	}
	registered := make(chan string, 1)
	var once sync.Once
	state.For(db).SetPublish(func(ev hub.Event) {
		if ev.Topic == sessionlife.Topic {
			if meta, err := e.m.Store.ReadMeta(ev.Key); err == nil && meta.HarnessSession != "" {
				once.Do(func() { registered <- meta.ID })
			}
		}
	})
	t.Cleanup(func() { state.For(db).SetPublish(nil) })
	info, _ := e.start("long job SLEEP:60000")
	// Running is saved before the harness has a native session to resume.
	// Stop only after that session ID has committed.
	select {
	case id := <-registered:
		if id != info.ID {
			t.Fatalf("registered session = %s; want %s", id, info.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the harness session was not registered")
	}
	turn, err := e.m.Stop(e.ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != TurnStopped || !turn.StopAsked {
		t.Fatalf("stopped turn = %#v", turn)
	}
	got, _ := e.wait(info.ID, 1)
	if got.Status != StatusIdle || got.HarnessSession == "" {
		t.Fatalf("stopped session = status %s, harness %q; want idle and resumable", got.Status, got.HarnessSession)
	}
	if _, err := e.m.Stop(e.ctx, info.ID); err == nil {
		t.Fatal("stopping an idle session should fail")
	}
	if _, err := e.send(info.ID, "SLEEP:0 RESPOND: back again", WakeOptions{}); err != nil {
		t.Fatalf("send after stop: %v", err)
	}
	_, second := e.wait(info.ID, 2)
	if second.Result != "back again" || second.HarnessSession != got.HarnessSession {
		t.Fatalf("resumed turn = %#v", second)
	}
}

func TestCloseEndsSessionAndFreesName(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ok", func(o *StartOptions) { o.Name = "scout" })
	e.wait(info.ID, 1)
	e.doneGoals()
	closed, err := e.m.Close(e.ctx, "scout")
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("Close = %#v, %v", closed, err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 more", WakeOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("send to closed session err = %v, want ErrClosed", err)
	}
	if open, _ := e.m.Store.List(false); len(open) != 0 {
		t.Fatalf("closed session still listed as open: %#v", open)
	}
	if all, _ := e.m.Store.List(true); len(all) != 1 || all[0].Status != StatusClosed {
		t.Fatalf("--all listing = %#v", all)
	}
	// The name is free again, and the old session stays reachable by id only.
	if _, err := e.m.Store.Resolve("scout"); err == nil {
		t.Fatal("closed session still resolves by name")
	}
	again, _ := e.start("SLEEP:1 RESPOND: ok", func(o *StartOptions) { o.Name = "scout" })
	if got, err := e.m.Store.Resolve("scout"); err != nil || got.ID != again.ID {
		t.Fatalf("name resolves to %#v, %v", got, err)
	}
	e.wait(again.ID, 1)
}

func TestCloseStopsARunningTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("job SLEEP:60000")
	e.waitStatus(info.ID, StatusRunning)
	e.doneGoals()
	closed, err := e.m.Close(e.ctx, info.ID)
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("Close = %#v, %v", closed, err)
	}
	turn, _ := e.m.Store.ReadTurn(info.ID, 1)
	if turn.Status != TurnStopped || !turn.StopAsked || turn.StopReason != "close" {
		t.Fatalf("turn after close = %#v", turn)
	}
}

func TestNamesAreUniqueAmongOpenSessionsAndLookupByName(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: a", func(o *StartOptions) { o.Name = "alpha" })
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Name: "alpha", Dir: t.TempDir(), Task: "x"}); err == nil || !strings.Contains(err.Error(), "already named") {
		t.Fatalf("duplicate name err = %v", err)
	}
	for _, bad := range []string{"has space", "ses_looks-like-an-id", "-lead", ""} {
		if bad == "" {
			continue
		}
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) accepted", bad)
		}
	}
	got, err := e.m.Store.Resolve("alpha")
	if err != nil || got.ID != a.ID {
		t.Fatalf("Resolve(alpha) = %#v, %v", got, err)
	}
	if got, err := e.m.Store.Resolve(a.ID); err != nil || got.Name != "alpha" {
		t.Fatalf("Resolve(id) = %#v, %v", got, err)
	}
	if _, err := e.m.Store.Resolve("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(nobody) err = %v", err)
	}
	e.wait(a.ID, 1)
}

func TestMaxDepthCapsNesting(t *testing.T) {
	e := newTestEnv(t)
	parent, _ := e.start("SLEEP:1 RESPOND: parent")
	e.wait(parent.ID, 1)
	if _, err := e.m.Store.UpdateMeta(parent.ID, func(m *Meta) { m.Depth = 2 }); err != nil {
		t.Fatal(err)
	}
	// A session started from inside a depth-2 session is depth 3: allowed.
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, parent.ID)
	t.Setenv(mode.EnvDepth, "2")
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	if info.Depth != 3 || info.ParentSession != parent.ID {
		t.Fatalf("nested session = depth %d parent %q", info.Depth, info.ParentSession)
	}
	e.wait(info.ID, 1)
	// From depth 3, the next would be 4 > subagent.maxDepth (3): refused.
	t.Setenv(mode.EnvDepth, "3")
	if _, err := e.m.Store.UpdateMeta(parent.ID, func(m *Meta) { m.Depth = 3 }); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(), Task: "deeper"})
	if err == nil || !strings.Contains(err.Error(), "subagent.maxDepth") || !strings.Contains(err.Error(), "depth 4") {
		t.Fatalf("depth-4 start err = %v", err)
	}
	// The cap is configurable.
	e.m.Settings = func() settings.Values {
		v := settings.Defaults()
		v.SubagentMaxDepth = 4
		return v
	}
	deeper, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(), Task: "SLEEP:1 RESPOND: ok"})
	if err != nil || deeper.Depth != 4 {
		t.Fatalf("depth-4 start with maxDepth 4 = %#v, %v", deeper, err)
	}
	e.wait(deeper.ID, 1)
}

func TestIdleSessionsNeverExpire(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	// Pretend the session has been idle for over a year.
	old := time.Now().AddDate(-1, -1, 0)
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.Created, m.LastActivity = old, old }); err != nil {
		t.Fatal(err)
	}
	open, err := e.m.Store.List(false)
	if err != nil || len(open) != 1 || open[0].ID != info.ID || open[0].Status != StatusIdle {
		t.Fatalf("year-old idle session = %#v, %v", open, err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: still here", WakeOptions{}); err != nil {
		t.Fatalf("send to old idle session: %v", err)
	}
	if _, done := e.wait(info.ID, 2); done.Result != "still here" {
		t.Fatalf("turn = %#v", done)
	}
}

func TestNoTurnCap(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: 1")
	e.wait(info.ID, 1)
	for n := 2; n <= 12; n++ {
		if _, err := e.send(info.ID, "SLEEP:1 RESPOND: again", WakeOptions{}); err != nil {
			t.Fatalf("turn %d refused: %v", n, err)
		}
		e.wait(info.ID, n)
	}
	if got, _ := e.m.Store.Info(info.ID); got.Turns != 12 {
		t.Fatalf("turns = %d", got.Turns)
	}
}

func TestWorkflowWorkersCannotStartOrControlSubagents(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	t.Setenv(mode.EnvMode, mode.Workflow)
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(), Task: "x"}); err == nil || err.Error() != "subagent start is owner or root or session only; use subagent mode to start work" {
		t.Fatalf("Start in workflow mode err = %v", err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 x", WakeOptions{}); err == nil || err.Error() != "subagent start is owner or root or session only; use subagent mode to start work" {
		t.Fatalf("Send in workflow mode err = %v", err)
	}
	if _, err := e.m.Stop(e.ctx, info.ID); err == nil || err.Error() != "subagent stop is owner or root or session only; use subagent mode to control sessions" {
		t.Fatalf("Stop in workflow mode err = %v", err)
	}
	if _, err := e.m.Close(e.ctx, info.ID); err == nil || err.Error() != "subagent close is owner or root or another session only; a session becomes dormant when its turn ends, so it does not close itself" {
		t.Fatalf("Close in workflow mode err = %v", err)
	}
	// Read-only access still works.
	if _, err := e.m.Store.List(true); err != nil {
		t.Fatal(err)
	}
	if got, t2, err := e.m.Wait(e.ctx, info.ID, 1); err != nil || got.ID != info.ID || t2.Result != "ok" {
		t.Fatalf("Wait in workflow mode = %#v %#v %v", got, t2, err)
	}
}

func TestSendRefusesWhenHarnessCannotResume(t *testing.T) {
	custom := profile.Profile{
		Name: "custom", Harness: profile.HarnessCustom, Default: true, Taste: 5, Intelligence: 5, Cost: 5,
		Command: []string{"/bin/sh", "-c", `cat >/dev/null; printf done`},
	}
	e := newTestEnv(t, custom)
	info, _ := e.start("task", func(o *StartOptions) { o.Profile = "custom" })
	if info.Resumable {
		t.Fatal("custom harness reported resumable")
	}
	_, done := e.wait(info.ID, 1)
	if done.Status != TurnOK || done.Result != "done" {
		t.Fatalf("single turn = %#v", done)
	}
	_, err := e.send(info.ID, "again", WakeOptions{})
	if err == nil || !strings.Contains(err.Error(), "no exact-session resume") || !strings.Contains(err.Error(), "fresh agent") {
		t.Fatalf("send err = %v; want a clear refusal, not a fresh agent", err)
	}
	if got, _ := e.m.Store.Info(info.ID); got.Turns != 1 {
		t.Fatalf("a refused send created turn %d", got.Turns)
	}
}

func TestOrphanedTurnIsRepairedAndSessionStaysUsable(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	// Simulate a runner that died mid-turn: a running record with no lock holder.
	if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(tr *Turn) { tr.Status = TurnRunning; tr.Ended = time.Time{} }); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Store.Info(info.ID)
	if err != nil || got.Status != StatusIdle {
		t.Fatalf("Info = %#v, %v", got, err)
	}
	if tr, _ := e.m.Store.ReadTurn(info.ID, 1); tr.Status != TurnError || !strings.Contains(tr.Error, "runner exited") {
		t.Fatalf("orphaned turn = %#v", tr)
	}
}

func TestStartRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	for name, tc := range map[string]struct {
		so   StartOptions
		want string
	}{
		"no goal":         {StartOptions{Profile: "mock", Dir: t.TempDir(), Task: "x"}, "--goal is required"},
		"blank goal":      {StartOptions{Goal: "  ", Profile: "mock", Dir: t.TempDir(), Task: "x"}, "--goal is required"},
		"no brief":        {StartOptions{Goal: "g", Profile: "mock", Dir: t.TempDir()}, "a brief is required"},
		"unknown profile": {StartOptions{Goal: "g", Profile: "nope", Dir: t.TempDir(), Task: "x"}, "unknown profile"},
		"bad dir":         {StartOptions{Goal: "g", Profile: "mock", Dir: filepath.Join(t.TempDir(), "missing"), Task: "x"}, "not a directory"},
	} {
		if _, _, err := e.m.Start(context.Background(), tc.so); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if st, _ := e.m.goalStore().Read(); len(st.Rooms) != 0 {
		t.Fatalf("rejected starts left goals behind: %#v", st.Rooms)
	}
	if sessions, _ := e.m.Store.List(true); len(sessions) != 0 {
		t.Fatalf("rejected starts left sessions behind: %#v", sessions)
	}
}

func TestConcurrentSendsNeverStartTwoTurns(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	var started atomic.Int32
	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			res, err := e.send(info.ID, "SLEEP:300 more", WakeOptions{})
			if err == nil && !res.Steered {
				started.Add(1)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	// Exactly one send started a turn; the others steered it or (if it had
	// already finished) started later turns one at a time, never concurrently.
	if started.Load() < 1 {
		t.Fatal("no send started a turn")
	}
	if got := e.waitStatus(info.ID, StatusIdle); got.Status != StatusIdle {
		t.Fatalf("session status = %s, want idle", got.Status)
	}
	turns, _ := e.m.Store.Turns(info.ID)
	for _, tr := range turns {
		if !tr.Done() {
			t.Fatalf("turn %d left unfinished: %#v", tr.N, tr)
		}
	}
}

// A session record without room identity comes from an older dyna. It must
// fail closed instead of being routed to the global room.
func TestReadMetaRejectsRecordWithoutRoom(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ok")
	e.wait(info.ID, 1)
	b, err := recordtest.ReadRaw(t, e.m.Store.Root, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "roomId")
	delete(raw, "room")
	raw["inboxId"], raw["inboxPhrase"] = "global", "global"
	b, _ = json.Marshal(raw)
	recordtest.WriteRaw(t, e.m.Store.Root, info.ID, b)
	if _, err := e.m.Store.ReadMeta(info.ID); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("ReadMeta(old record) err = %v, want ErrNoRoom", err)
	}
	if _, err := e.m.Store.Resolve("global:" + info.Name); err == nil {
		t.Fatal("an old record still resolves by address")
	}
}

// Phase-B gate: many senders post while turns start, run, and end. Every
// post reaches the session exactly once, either drained into a turn prompt
// or steered into a running turn, and no turn is left unfinished.
func TestConcurrentPostsAreDeliveredExactlyOnce(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	active, err := e.m.beginTurn(info.ID, "active turn", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.lock.Close()
	e.m.Spawner = runner
	meta, _ := e.m.Store.ReadMeta(info.ID)
	gate := e.m.Store.newTurnGate(e.m.rooms(), meta, active.N, meta.RoomCursor)
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, active.N, func(turn *Turn) { turn.Status = TurnRunning }); err != nil {
		t.Fatal(err)
	}
	const senders, perSender = 6, 20
	posted := map[string]bool{}
	postBatch := func(first, last int) {
		t.Helper()
		ids := make(chan string, senders*(last-first))
		var sendersDone sync.WaitGroup
		for sender := range senders {
			sendersDone.Add(1)
			go func(sender int) {
				defer sendersDone.Done()
				for i := first; i < last; i++ {
					msg, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: fmt.Sprintf("SLEEP:0 tok-%d-%d", sender, i)})
					if err != nil {
						t.Error(err)
						return
					}
					ids <- msg.ID
				}
			}(sender)
		}
		sendersDone.Wait()
		close(ids)
		for id := range ids {
			posted[id] = true
		}
	}
	postBatch(0, perSender/2)
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	live, cursor, err := gate.Read(0, false)
	if err != nil || len(live) != senders*perSender/2 {
		t.Fatalf("live gate: %d posts, %v", len(live), err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, active.N, func(turn *Turn) {
		for _, msg := range live {
			turn.Steering = append(turn.Steering, Steer{ID: msg.ID, TS: msg.TS, Message: msg.Message, Delivered: true})
		}
	}); err != nil {
		t.Fatal(err)
	}
	if final, _, err := gate.Read(0, true); err != nil || len(final) != 0 {
		t.Fatalf("seal: %+v %v", final, err)
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, active.N, func(turn *Turn) { turn.Status, turn.Ended = TurnOK, time.Now() }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.RoomCursor = cursor }); err != nil {
		t.Fatal(err)
	}
	held.lock.Close()
	postBatch(perSender/2, perSender)
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.wait(info.ID, active.N+1)
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	turns, err := e.m.Store.Turns(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, turn := range turns {
		if turn.Status != TurnOK {
			t.Fatalf("turn %d: %s", turn.N, turn.Status)
		}
		for _, steer := range turn.Steering {
			if !steer.Delivered {
				t.Fatalf("unread post %s", steer.ID)
			}
			seen[steer.ID]++
		}
	}
	if len(posted) != senders*perSender || len(seen) != len(posted) || len(turns) != 3 {
		t.Fatalf("posted %d, delivered %d over %d turns", len(posted), len(seen), len(turns))
	}
	for id := range posted {
		if seen[id] != 1 {
			t.Errorf("post %s delivered %d times", id, seen[id])
		}
	}
}

// A turn whose runner died never advanced the room cursor. The next turn
// re-reads from the old cursor and skips the posts the dead turn already
// delivered.
func TestNextTurnSkipsPostsADeadTurnDelivered(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	e.wait(info.ID, 1)
	before, _ := e.m.Store.ReadMeta(info.ID)
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: first", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, second := e.wait(info.ID, 2)
	if len(second.Steering) != 1 {
		t.Fatalf("turn 2 steering = %#v", second.Steering)
	}
	// Simulate the crash: the cursor stays where turn 2 started.
	if _, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) { m.RoomCursor = before.RoomCursor }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: second", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, third := e.wait(info.ID, 3)
	if len(third.Steering) != 1 || strings.Contains(third.Prompt, "RESPOND: first") || !strings.Contains(third.Prompt, "RESPOND: second") {
		t.Fatalf("turn 3 redelivered a post: %#v", third)
	}
}

func TestWakeAfterPostWasDeliveredDoesNotStartEmptyTurn(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("job SLEEP:20000")
	e.waitStatus(info.ID, StatusRunning)
	msg, err := e.m.RoomStore().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "finish before the sender wakes"})
	if err != nil {
		t.Fatal(err)
	}
	_, done := e.wait(info.ID, 1)
	if len(done.Steering) != 1 || done.Steering[0].ID != msg.ID || !done.Steering[0].Delivered {
		t.Fatalf("post did not reach turn 1: %+v", done)
	}
	// The worker finished before the sender reached Wake. Both initial and
	// retried delivery calls must report turn 1 without spawning turn 2.
	for i := 0; i < 2; i++ {
		woke, err := e.m.Wake(e.ctx, info.ID, WakeOptions{MessageID: msg.ID})
		if err != nil || !woke.Steered || woke.Turn != 1 {
			t.Fatalf("late wake = %+v, %v", woke, err)
		}
	}
	// Exercise the locked recheck as well: delivery may finish after Wake's
	// optimistic lookup but before beginTurnForPost acquires the turn lock.
	got, reused, err := e.m.beginTurnForPost(info.ID, "", "", nil, msg.ID)
	if err != nil || !reused || got.N != 1 {
		t.Fatalf("locked recheck = %+v, %v, %v", got, reused, err)
	}
	current, err := e.m.Store.Info(info.ID)
	if err != nil || current.Turns != 1 || current.Status != StatusIdle {
		t.Fatalf("late wake created an empty turn: %+v, %v", current, err)
	}
}

func TestNonResumableSchemaRetryKeepsWorkerContextLast(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "prompt")
	e := newTestEnv(t, profile.Profile{
		Name: "custom", Harness: profile.HarnessCustom, Taste: 5, Intelligence: 5, Cost: 5,
		Command: []string{"/bin/sh", "-c", `
count=0
if [ -f "$COUNT" ]; then count=$(/bin/cat "$COUNT"); fi
count=$((count+1))
printf '%s' "$count" > "$COUNT"
/bin/cat > "$CAPTURE.$count"
if [ "$count" -eq 1 ]; then printf '"invalid"'; else printf '{"n":7}'; fi
`},
		Env: map[string]string{"COUNT": filepath.Join(dir, "count"), "CAPTURE": capture},
	})
	info, _ := e.start("Produce the requested number.", func(o *StartOptions) {
		o.Profile = "custom"
		o.Schema = `{"type":"object","required":["n"],"properties":{"n":{"type":"number"}}}`
	})
	_, turn := e.wait(info.ID, 1)
	if turn.Status != TurnOK {
		t.Fatalf("schema retry failed: %#v", turn)
	}
	for _, attempt := range []string{"1", "2"} {
		data, err := os.ReadFile(capture + "." + attempt)
		if err != nil {
			t.Fatal(err)
		}
		prompt := string(data)
		contextAt := strings.LastIndex(prompt, "\n\n[DYNA WORKER CONTEXT]\n")
		if !strings.HasSuffix(prompt, "\n[/DYNA WORKER CONTEXT]") || strings.Index(prompt, "OUTPUT FORMAT:") >= contextAt {
			t.Fatalf("attempt %s misplaced context:\n%s", attempt, prompt)
		}
	}
}

// These fixture helpers exercise the same allocation path as the engine.
func (m *Manager) beginTurn(id, prompt, schema string, owe []string) (Turn, error) {
	turn, _, err := m.beginTurnConfigured(id, prompt, schema, owe, "", "", false, time.Time{})
	return turn, err
}

func (m *Manager) beginTurnForPost(id, prompt, schema string, owe []string, messageID string) (Turn, bool, error) {
	return m.beginTurnConfigured(id, prompt, schema, owe, messageID, "", false, time.Time{})
}
