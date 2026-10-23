package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

// Routing tests choose when a captured turn runs, so slow scheduling cannot
// make harness completion race a per-turn wall-clock deadline.
type pendingSessionTurn struct {
	manager *subagent.Manager
	id      string
	n       int
	lock    *os.File
}

type sessionFixtureSpawner struct {
	ctx     context.Context
	pending chan pendingSessionTurn
}

func (s sessionFixtureSpawner) Spawn(m *subagent.Manager, id string, n int, lock *os.File) error {
	select {
	case s.pending <- pendingSessionTurn{manager: m, id: id, n: n, lock: lock}:
		return nil
	case <-s.ctx.Done():
		lock.Close()
		return s.ctx.Err()
	}
}

func runSessionTurn(t *testing.T, ctx context.Context, pending <-chan pendingSessionTurn, n int) {
	t.Helper()
	select {
	case turn := <-pending:
		if turn.n != n {
			turn.lock.Close()
			t.Fatalf("queued turn %d, want %d", turn.n, n)
		}
		if err := turn.manager.RunTurn(ctx, turn.id, turn.n, turn.lock); err != nil {
			t.Fatalf("turn %d: %v", n, err)
		}
	case <-ctx.Done():
		t.Fatalf("turn %d was not queued: %v", n, ctx.Err())
	}
}

func sessionWorkerFixture(t *testing.T, ctx context.Context, pending chan pendingSessionTurn) (*subagent.Manager, room.Record) {
	t.Helper()
	// Session scratch must be removed with the fixture after its runners stop.
	t.Setenv("TMPDIR", t.TempDir())
	t.Cleanup(func() {
		// Worker defers finish before cleanup. Release queued locks on failed assertions.
		for {
			select {
			case turn := <-pending:
				turn.lock.Close()
			default:
				return
			}
		}
	})
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Default: true}}}
	m := &subagent.Manager{
		Store:    &subagent.Store{Root: filepath.Join(t.TempDir(), "sessions")},
		Rooms:    &room.Store{Root: filepath.Join(t.TempDir(), "rooms")},
		Profiles: func() (*profile.Store, error) { return profiles, nil },
		Settings: settings.Defaults,
		Spawner:  sessionFixtureSpawner{ctx, pending},
	}
	rec, err := m.Rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	return m, rec
}

func TestSessionWorkerPostEventBurstNoWakeAndDormant(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	pending := make(chan pendingSessionTurn, 4)
	m, rec := sessionWorkerFixture(t, ctx, pending)
	info, _, err := m.Start(context.Background(), subagent.StartOptions{Goal: "Test queued delivery", Profile: "mock", Name: "target", Room: rec.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	runSessionTurn(t, ctx, pending, 1)
	if _, err := m.Store.UpdateMeta(info.ID, func(meta *subagent.Meta) { meta.Finished = time.Now(); meta.Status = subagent.StatusDormant }); err != nil {
		t.Fatal(err)
	}
	signals := newWorkerSignals()
	ticked := make(chan struct{}, 4)
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWorker(workerCtx, signals.channels["sessions"], func(ctx context.Context) error {
			err := deliverSessions(ctx, m, signals)
			ticked <- struct{}{}
			return err
		}, func(err error) { t.Error(err) })
	}()
	defer func() { stopWorker(); <-done }()
	var posts []roomsvc.Post
	tick := func() {
		t.Helper()
		signals.wake(roomsvc.Event{Kind: "posts", Posts: posts})
		select {
		case <-ticked:
		case <-ctx.Done():
			t.Fatal("post event did not run the session worker")
		}
	}
	post := func(quiet bool) room.Message {
		t.Helper()
		msg, err := m.Rooms.Post(rec.ID, room.PostInput{Wake: !quiet, From: rec.Name, To: []string{info.Address()}, SessionIDs: map[string]string{info.Address(): info.ID}, Text: "SLEEP:0\nRESPOND: queued"})
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, roomsvc.Post{Message: msg})
		return msg
	}
	post(true)
	meta, _ := m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatalf("NoWake started turn %d", meta.Turns)
	}
	for range 2 {
		post(false)
	}
	meta, _ = m.Store.ReadMeta(info.ID)
	if meta.Turns != 1 {
		t.Fatalf("posting started turn %d without the engine", meta.Turns)
	}
	// There is no minute clock. Only this post event can deliver the burst.
	tick()
	// Keep the runner queued while duplicate post events arrive.
	for range 3 {
		tick()
	}
	if len(pending) != 1 {
		t.Fatalf("post burst spawned %d turns; want one", len(pending))
	}
	runSessionTurn(t, ctx, pending, 2)
	turn, err := m.Store.ReadTurn(info.ID, 2)
	if err != nil || len(turn.Steering) != 3 {
		t.Fatalf("burst delivery: %+v %v", turn, err)
	}
	for _, steer := range turn.Steering {
		if !steer.Delivered {
			t.Fatalf("unread post: %+v", steer)
		}
	}
	for range 3 {
		tick()
	}
	meta, _ = m.Store.ReadMeta(info.ID)
	if meta.Turns != 2 || !meta.Finished.IsZero() {
		t.Fatalf("burst must resume dormancy once: %+v", meta)
	}
	stopWorker()
	<-done
	post(false) // This post stays stored while no session worker runs.
	restarted, stopRestart := context.WithCancel(ctx)
	restartDone := make(chan struct{})
	w := testWorkers(nil)
	restartSignals := newWorkerSignals()
	w.sessions = func(ctx context.Context) error { return deliverSessions(ctx, m, restartSignals) }
	go func() {
		defer close(restartDone)
		w.run(restarted, restartSignals, newFakeClock().timers(restarted), func(_ string, err error) { t.Error(err) })
	}()
	defer func() { stopRestart(); <-restartDone }()
	runSessionTurn(t, ctx, pending, 3)
}

type heldSessionSpawner struct{ lock *os.File }

func (s *heldSessionSpawner) Spawn(_ *subagent.Manager, _ string, _ int, lock *os.File) error {
	s.lock = lock
	return nil
}

func TestSessionMemberEventDeliversQueuedPostAfterSilentTurn(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	pending := make(chan pendingSessionTurn, 4)
	m, rec := sessionWorkerFixture(t, ctx, pending)
	info, _, err := m.Start(context.Background(), subagent.StartOptions{Goal: "Test queued input", Profile: "mock", Name: "target", Room: rec.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	runSessionTurn(t, ctx, pending, 1)
	post := func(text string) room.Message {
		t.Helper()
		msg, err := m.Rooms.Post(rec.ID, room.PostInput{Wake: true, From: rec.Name, To: []string{info.Address()}, SessionIDs: map[string]string{info.Address(): info.ID}, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	signals := newWorkerSignals()
	tickPost := func(msg room.Message) {
		t.Helper()
		signals.wake(roomsvc.Event{Kind: "posts", Posts: []roomsvc.Post{{Message: msg}}})
		if err := deliverSessions(ctx, m, signals); err != nil {
			t.Fatal(err)
		}
	}
	held := &heldSessionSpawner{}
	m.Spawner = held
	t.Cleanup(func() {
		if held.lock != nil {
			held.lock.Close()
		}
	})
	active := post("SLEEP:0\nRESPOND: active")
	tickPost(active)
	cutoff, err := m.Rooms.FileSize(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	late := post("SLEEP:0\nRESPOND: next")
	tickPost(late) // The queued runner has not read this input.
	meta, _ := m.Store.ReadMeta(info.ID)
	if meta.Turns != 2 {
		t.Fatalf("queued post started another turn: %+v", meta)
	}
	if _, err := m.Store.UpdateTurn(info.ID, 2, func(turn *subagent.Turn) {
		turn.Status = subagent.TurnOK
		turn.Ended = time.Now()
		turn.Reported = true
		turn.ReportTo = nil // A silent turn emits no completion post.
		// The held spawner never runs the drainer. Model its delivered input
		// together with completion, so claim recovery cannot replay it.
		turn.Steering = []subagent.Steer{{ID: active.ID, TS: active.TS, Delivered: true}}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.UpdateMeta(info.ID, func(meta *subagent.Meta) { meta.RoomCursor = cutoff; meta.WrapUpAfter = 2 }); err != nil {
		t.Fatal(err)
	}
	if err := held.lock.Close(); err != nil {
		t.Fatal(err)
	}
	held.lock = nil
	m.Spawner = sessionFixtureSpawner{ctx, pending}
	// No startup pass or fallback clock can start the continuation.
	signals.wake(roomsvc.Event{Kind: "members", RoomID: rec.ID, Change: &listchange.Change{Op: "upsert", ID: info.ID}})
	if err := deliverSessions(ctx, m, signals); err != nil {
		t.Fatal(err)
	}
	runSessionTurn(t, ctx, pending, 3)
	turn, err := m.Store.ReadTurn(info.ID, 3)
	if err != nil || len(turn.Steering) != 1 || turn.Steering[0].ID != late.ID || !turn.Steering[0].Delivered {
		t.Fatalf("queued input was not delivered once: %+v %v", turn, err)
	}
}

func TestOwnerPostEventStartsInternalRootWithoutFallback(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	pending := make(chan pendingSessionTurn, 4)
	m, rec := sessionWorkerFixture(t, ctx, pending)
	rec.Repo = t.TempDir()
	root, _, err := m.StartRoot(rec, "SLEEP:0\nRESPOND: ready", "")
	if err != nil {
		t.Fatal(err)
	}
	runSessionTurn(t, ctx, pending, 1)
	msg, err := m.Rooms.Post(rec.ID, room.PostInput{Wake: true, Human: true, From: rec.Name, To: []string{rec.Name}, Text: "SLEEP:0\nRESPOND: answer"})
	if err != nil {
		t.Fatal(err)
	}
	signals := newWorkerSignals()
	ticked := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWorker(ctx, signals.channels["sessions"], func(ctx context.Context) error {
			err := deliverSessions(ctx, m, signals)
			ticked <- struct{}{}
			return err
		}, func(err error) { t.Error(err) })
	}()
	defer func() { cancel(); <-done }()
	// No startup recovery or fallback clock runs. The stored post event is
	// the only signal, and it contains no binding for the internal root.
	signals.wake(roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{{Message: msg}}})
	select {
	case <-ticked:
	case <-ctx.Done():
		t.Fatal("owner post did not signal session delivery")
	}
	meta, err := m.Store.ReadMeta(root.ID)
	if err != nil || meta.Turns != 2 {
		t.Fatalf("owner post did not start root from its event: turns=%d, %v", meta.Turns, err)
	}
	runSessionTurn(t, ctx, pending, 2)
	turn, err := m.Store.ReadTurn(root.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Steering) != 1 || turn.Steering[0].ID != msg.ID || !turn.Steering[0].Delivered {
		t.Fatalf("root input was not delivered: %+v", turn.Steering)
	}
}

func TestSessionWorkerFixtureRunsHarnessOnlyWhenDriven(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	pending := make(chan pendingSessionTurn, 4)
	m, rec := sessionWorkerFixture(t, ctx, pending)
	info, _, err := m.Start(context.Background(), subagent.StartOptions{Goal: "Test explicit harness scheduling", Profile: "mock", Name: "controlled", Room: rec.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.Store.ReadTurn(info.ID, 1)
	if err != nil || before.Status != subagent.TurnQueued {
		t.Fatalf("fixture ran before the test drove it: %+v %v", before, err)
	}
	runSessionTurn(t, ctx, pending, 1)
	result, err := m.Store.ReadTurn(info.ID, 1)
	if err != nil || result.Status != subagent.TurnOK {
		t.Fatalf("controlled turn: %+v %v", result, err)
	}
}

// The safety sweep counts input that no event delivered. A post whose event
// is in the same batch, and the startup catch-up, are not findings.
func TestSafetySweepCountsInputNoEventDelivered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	pending := make(chan pendingSessionTurn, 4)
	m, rec := sessionWorkerFixture(t, ctx, pending)
	info, _, err := m.Start(context.Background(), subagent.StartOptions{Goal: "Test the safety sweep", Profile: "mock", Name: "target", Room: rec.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	runSessionTurn(t, ctx, pending, 1)
	signals := newWorkerSignals()
	// plant stores a wake post without its event, as a writer that bypassed
	// the engine would.
	plant := func() room.Message {
		t.Helper()
		msg, err := m.Rooms.Post(rec.ID, room.PostInput{Wake: true, From: rec.Name, To: []string{info.Address()}, SessionIDs: map[string]string{info.Address(): info.ID}, Text: "SLEEP:0\nRESPOND: planted"})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	pass := func(n int) {
		t.Helper()
		if err := deliverSessions(ctx, m, signals); err != nil {
			t.Fatal(err)
		}
		runSessionTurn(t, ctx, pending, n)
	}
	plant()
	signals.sweep("sessions")
	pass(2)
	if got := signals.findings(); got != 1 {
		t.Fatalf("findings after a planted post = %d, want 1", got)
	}
	msg := plant()
	signals.wake(roomsvc.Event{Kind: "posts", RoomID: rec.ID, Posts: []roomsvc.Post{{Message: msg}}})
	signals.sweep("sessions")
	pass(3)
	plant()
	signals.recoverSessions()
	pass(4)
	if got := signals.findings(); got != 1 {
		t.Fatalf("findings after an evented post and the startup pass = %d, want 1", got)
	}
}
