package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// testEnv is an isolated data dir with one manager that runs mock turns
// in-process, plus the router over the same stores.
type testEnv struct {
	t           *testing.T
	svc         *Service
	mgr         *subagent.Manager
	ctx         context.Context
	rooms       int
	waitForTurn func(context.Context, string, int) error
}

func newTestEnv(t *testing.T, options ...func(*testEnv)) *testEnv {
	t.Helper()
	storetest.Isolate(t)
	t.Setenv(runstore.AgentJournalRootEnv, "")
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	spawner := &testTurnSpawner{ctx: ctx}
	// Stop every runner before the data dir is removed, including internal
	// roots that a human post to @root starts.
	mgr := &subagent.Manager{
		Store:    subagent.DefaultStore(),
		Profiles: func() (*profile.Store, error) { return profiles, nil },
		Settings: settings.Defaults,
		Spawner:  spawner,
	}
	t.Cleanup(func() {
		cancel()
		spawner.running.Wait()
		if err := database.Close(filepath.Dir(mgr.Store.Root)); err != nil {
			t.Error(err)
		}
	})
	e := &testEnv{t: t, svc: New(room.DefaultStore(), mgr), mgr: mgr, ctx: ctx, waitForTurn: spawner.wait}
	for _, option := range options {
		option(e)
	}
	return e
}

// withImmediateTurns is for terminal-report tests that do not steer live turns.
// Keep the real runner, but skip the mock hold and the manager's wait polling.
func withImmediateTurns(e *testEnv) {
	e.mgr.Spawner.(*testTurnSpawner).immediate = true
}

// testTurnSpawner reports runner completion without the manager's poll delay.
// Ordinary mock turns keep their hold so tests can still steer running turns.
type testTurnSpawner struct {
	ctx       context.Context
	immediate bool
	done      sync.Map // [session ID, turn number] -> runner completion channel
	running   sync.WaitGroup
}

func (s *testTurnSpawner) Spawn(m *subagent.Manager, id string, n int, lock *os.File) error {
	if s.immediate {
		if _, err := m.Store.UpdateTurn(id, n, func(turn *subagent.Turn) {
			if turn.Profile == "mock" {
				turn.Prompt = "SLEEP:0\n" + turn.Prompt
			}
		}); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	s.done.Store(fmt.Sprintf("%s/%d", id, n), done)
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		defer close(done)
		_ = m.RunTurn(s.ctx, id, n, lock)
	}()
	return nil
}

func (s *testTurnSpawner) wait(ctx context.Context, id string, n int) error {
	value, ok := s.done.Load(fmt.Sprintf("%s/%d", id, n))
	if !ok {
		return fmt.Errorf("turn runner was not started")
	}
	select {
	case <-value.(chan struct{}):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *testEnv) room() room.Record {
	e.t.Helper()
	e.rooms++
	rec, err := e.svc.Rooms.Create(room.CreateInput{Name: fmt.Sprintf("test-%d", e.rooms), CreatedBy: "test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

// as stamps the process as a session (or clears the stamp for nil) so that
// Start creates children of parent in its room.
func (e *testEnv) as(parent *subagent.Info) {
	storetest.ClearStamp(e.t)
	if parent != nil {
		e.t.Setenv(mode.EnvMode, mode.Subagent)
		e.t.Setenv(mode.EnvSession, parent.ID)
		e.t.Setenv(mode.EnvDepth, "1")
		e.t.Setenv(mode.EnvRoom, parent.Room)
		e.t.Setenv(mode.EnvRoomID, parent.RoomID)
	}
}

// start opens a session named name in rec, as a child of parent when set,
// and waits for its first turn so it is idle and resumable. Setup turns do
// not need the mock hold that allows steering during tests.
func (e *testEnv) start(rec room.Record, name string, parent *subagent.Info) subagent.Info {
	e.t.Helper()
	e.as(parent)
	defer e.as(nil)
	opts := subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: name, Dir: e.t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"}
	if parent == nil {
		opts.Room = rec.Name
	}
	info, _, err := e.mgr.Start(context.Background(), opts)
	if err != nil {
		e.t.Fatalf("Start %s: %v", name, err)
	}
	return e.wait(info.ID, 1)
}

func (e *testEnv) wait(id string, turn int) subagent.Info {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	// Drive the engine pass explicitly; the router itself must never spawn.
	if _, err := e.mgr.TickSessions(ctx); err != nil {
		e.t.Fatal(err)
	}
	if e.waitForTurn != nil {
		if err := e.waitForTurn(ctx, id, turn); err != nil {
			e.t.Fatalf("runner (%s, %d): %v", id, turn, err)
		}
	}
	info, _, err := e.mgr.Wait(ctx, id, turn)
	if err != nil {
		e.t.Fatalf("Wait(%s, %d): %v", id, turn, err)
	}
	return info
}

// fixture is two rooms: A has lead (top level) with children decoder and
// parser, and crc (top level); B has other (top level).
type fixture struct {
	*testEnv
	a, b                              room.Record
	lead, decoder, parser, crc, other subagent.Info
}

func newFixture(t *testing.T, options ...func(*testEnv)) *fixture {
	e := newTestEnv(t, options...)
	f := &fixture{testEnv: e, a: e.room(), b: e.room()}
	f.lead = e.start(f.a, "lead", nil)
	f.decoder = e.start(f.a, "decoder", &f.lead)
	f.parser = e.start(f.a, "parser", &f.lead)
	f.crc = e.start(f.a, "crc", nil)
	f.other = e.start(f.b, "other", nil)
	return f
}

func (f *fixture) session(info subagent.Info) Sender {
	return Sender{Kind: SenderSession, Room: info.Room, Session: info}
}

// newRun creates a run in room rec whose parent session is parent (or none).
func (f *testEnv) newRun(id string, rec room.Record, parent *subagent.Info) *runstore.Run {
	f.t.Helper()
	f.as(parent)
	f.t.Setenv(mode.EnvRoom, rec.Name)
	f.t.Setenv(mode.EnvRoomID, rec.ID)
	f.t.Setenv("DYNA_RUN_ID", id)
	run, err := runstore.Create("test", "return null", nil)
	f.as(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { run.Finish("ok", "null", nil) })
	return run
}

func sorted(addrs []string) []string {
	out := append([]string(nil), addrs...)
	sort.Strings(out)
	return out
}

// The grammar table for every sender class. Resolution has no side effects.
func TestResolveSenderClassTable(t *testing.T) {
	f := newFixture(t)
	a, b := f.a.Name, f.b.Name
	withParent := f.newRun("wf_with-parent", f.a, &f.lead)
	topRun := f.newRun("wf_top-level", f.a, nil)
	otherRun := f.newRun("wf_other-run", f.a, nil)

	owner := Sender{Kind: SenderOwner}
	human := Sender{Kind: SenderOwner, Room: a}
	script := Sender{Kind: SenderScript, Room: a, RunID: withParent.Meta.ID}
	topScript := Sender{Kind: SenderScript, Room: a, RunID: topRun.Meta.ID}
	worker := Sender{Kind: SenderWorker, Room: a, RunID: withParent.Meta.ID, Agent: 1}
	topWorker := Sender{Kind: SenderWorker, Room: a, RunID: topRun.Meta.ID, Agent: 1}

	cases := []struct {
		name     string
		snd      Sender
		mentions []string
		from     string
		to       []string
		err      string
	}{
		// External owner: no room context; must qualify.
		{"external bare name", owner, []string{"@decoder"}, "", nil, "no room context"},
		{"external bare selector", owner, []string{"@children"}, "", nil, "no room context"},
		{"external qualified name", owner, []string{"@" + a + ":lead"}, a, []string{a + ":lead"}, ""},
		{"external room owner", owner, []string{"@" + a}, a, []string{a}, ""},
		{"external qualified root", owner, []string{"@" + a + ":root"}, a, []string{a}, ""},
		{"external removed room", owner, []string{"@" + a + ":room"}, "", nil, "was removed"},
		{"external removed owner", owner, []string{"@" + a + ":owner"}, "", nil, "was removed"},
		{"external team is top level", owner, []string{"@" + a + ":team"}, a, []string{a + ":crc", a + ":lead"}, ""},
		{"external children", owner, []string{"@" + a + ":children"}, "", nil, "@team"},
		{"external parent", owner, []string{"@" + a + ":parent"}, "", nil, "no parent"},
		{"external siblings", owner, []string{"@" + a + ":siblings"}, "", nil, "@team"},
		// The desktop human is the owner with the open room as context.
		{"human bare name", human, []string{"@lead"}, a, []string{a + ":lead"}, ""},
		{"human owner", human, []string{"@root"}, a, []string{a}, ""},
		{"human removed room", human, []string{"@room"}, "", nil, "was removed"},
		{"human team", human, []string{"@team"}, a, []string{a + ":crc", a + ":lead"}, ""},
		{"human children", human, []string{"@children"}, "", nil, "@team"},
		// A child session.
		{"child parent", f.session(f.decoder), []string{"@parent"}, a + ":decoder", []string{a + ":lead"}, ""},
		{"child siblings", f.session(f.decoder), []string{"@siblings"}, a + ":decoder", []string{a + ":parser"}, ""},
		{"child no children", f.session(f.decoder), []string{"@children"}, "", nil, "resolved to nobody"},
		{"child owner", f.session(f.decoder), []string{"@root"}, a + ":decoder", []string{a}, ""},
		{"child team", f.session(f.decoder), []string{"@team"}, "", nil, "only the room owner"},
		{"child bare member", f.session(f.decoder), []string{"@crc"}, a + ":decoder", []string{a + ":crc"}, ""},
		{"child self", f.session(f.decoder), []string{"@decoder"}, "", nil, "is you"},
		{"child cross room", f.session(f.decoder), []string{"@" + b + ":other"}, a + ":decoder", []string{b + ":other"}, ""},
		{"child selector in other room", f.session(f.decoder), []string{"@" + b + ":siblings"}, "", nil, "own room"},
		{"child bare other room owner", f.session(f.decoder), []string{"@" + b}, a + ":decoder", []string{b}, ""},
		{"child unknown", f.session(f.decoder), []string{"@nobody"}, "", nil, "no open session"},
		// A top-level session.
		{"top parent is owner", f.session(f.lead), []string{"@parent"}, a + ":lead", []string{a}, ""},
		{"top children", f.session(f.lead), []string{"@children"}, a + ":lead", []string{a + ":decoder", a + ":parser"}, ""},
		{"top siblings", f.session(f.lead), []string{"@siblings"}, a + ":lead", []string{a + ":crc"}, ""},
		{"dedupe", f.session(f.lead), []string{"@decoder", "@children"}, a + ":lead", []string{a + ":decoder", a + ":parser"}, ""},
		// A workflow script posts as run:<id>.
		{"script parent session", script, []string{"@parent"}, "run:" + withParent.Meta.ID, []string{a + ":lead"}, ""},
		{"script top parent is owner", topScript, []string{"@parent"}, "run:" + topRun.Meta.ID, []string{a}, ""},
		{"script member", script, []string{"@crc"}, "run:" + withParent.Meta.ID, []string{a + ":crc"}, ""},
		{"script siblings", script, []string{"@siblings"}, "", nil, "@siblings"},
		{"script worker", script, []string{"@run:" + withParent.Meta.ID + "/agent:2"}, "run:" + withParent.Meta.ID, []string{"run:" + withParent.Meta.ID + "/agent:2"}, ""},
		// A workflow worker's shell never reaches a session.
		{"worker owner", worker, []string{"@root"}, "run:" + withParent.Meta.ID + "/agent:1", []string{a}, ""},
		{"worker parent session", worker, []string{"@parent"}, "", nil, "workflow"},
		{"worker top parent is owner", topWorker, []string{"@parent"}, "run:" + topRun.Meta.ID + "/agent:1", []string{a}, ""},
		{"worker member", worker, []string{"@lead"}, "", nil, "workflow"},
		{"worker qualified member", worker, []string{"@" + a + ":lead"}, "", nil, "workflow"},
		{"worker own run worker", worker, []string{"@run:" + withParent.Meta.ID + "/agent:2"}, "run:" + withParent.Meta.ID + "/agent:1", []string{"run:" + withParent.Meta.ID + "/agent:2"}, ""},
		{"worker other run", worker, []string{"@run:" + otherRun.Meta.ID + "/agent:1"}, "", nil, "own run"},
		{"worker self", worker, []string{"@run:" + withParent.Meta.ID + "/agent:1"}, "", nil, "is you"},
		// Grammar.
		{"no mention", f.session(f.lead), nil, "", nil, "@mention"},
		{"not a mention", f.session(f.lead), []string{"lead"}, "", nil, "start with @"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.svc.resolve(tc.snd, tc.mentions)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.from != tc.from || !reflect.DeepEqual(sorted(got.addrs()), sorted(tc.to)) {
				t.Fatalf("from %q to %v, want from %q to %v", got.from, got.addrs(), tc.from, tc.to)
			}
			if !reflect.DeepEqual(got.via, tc.mentions) {
				t.Fatalf("via = %v, want %v", got.via, tc.mentions)
			}
		})
	}
}

func TestPostRejectsMixedRoomsAndStoresNothing(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Post(f.ctx, PostInput{
		Sender: Sender{Kind: SenderOwner}, Mentions: []string{"@" + f.a.Name + ":lead", "@" + f.b.Name + ":other"}, Text: "both",
	})
	if err == nil || !strings.Contains(err.Error(), "one room") {
		t.Fatalf("err = %v", err)
	}
	for _, rec := range []room.Record{f.a, f.b} {
		msgs, _ := f.svc.Rooms.Messages(rec.ID)
		if len(msgs) != 0 {
			t.Fatalf("room %s stored %#v", rec.Name, msgs)
		}
	}
}

// A post to another room is stored in that room's log, not the sender's.
func TestCrossRoomPostLivesInRecipientRoom(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.Post(f.ctx, PostInput{Sender: f.session(f.decoder), Mentions: []string{"@" + f.b.Name}, Text: "hello b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.RoomID != f.b.ID || res.Message.From != f.a.Name+":decoder" {
		t.Fatalf("message = %#v", res.Message)
	}
	if msgs, _ := f.svc.Rooms.Messages(f.a.ID); len(msgs) != 0 {
		t.Fatalf("sender room stored %#v", msgs)
	}
}

func TestExplicitBadRecipientsRejectGroupMembersSkip(t *testing.T) {
	f := newFixture(t)
	if _, err := f.mgr.Store.UpdateMeta(f.parser.ID, func(m *subagent.Meta) { m.Resumable = false }); err != nil {
		t.Fatal(err)
	}
	lead := f.session(f.lead)
	// Explicit and not resumable: the whole post fails and nothing is stored.
	if _, err := f.svc.Post(f.ctx, PostInput{Sender: lead, Mentions: []string{"@decoder", "@parser"}, Text: "x"}); err == nil || !strings.Contains(err.Error(), "not resumable") {
		t.Fatalf("explicit err = %v", err)
	}
	if msgs, _ := f.svc.Rooms.Messages(f.a.ID); len(msgs) != 0 {
		t.Fatalf("stored after a resolution error: %#v", msgs)
	}
	// Closed explicit name: no open session has it.
	if _, err := f.mgr.CloseWithOptions(f.ctx, f.crc.ID, subagent.CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Post(f.ctx, PostInput{Sender: lead, Mentions: []string{"@crc"}, Text: "x"}); err == nil || !strings.Contains(err.Error(), "no open session") {
		t.Fatalf("closed err = %v", err)
	}
	// In a group the same member is skipped, and the post goes through.
	res, err := f.svc.Post(f.ctx, PostInput{Sender: lead, Mentions: []string{"@children"}, Text: "gate changed"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Message.To, []string{f.a.Name + ":decoder"}) || len(res.Skipped) != 1 || res.Skipped[0].To != f.a.Name+":parser" {
		t.Fatalf("result = %#v", res)
	}
	f.wait(f.decoder.ID, 2)
	// A group that leaves nobody fails.
	if _, err := f.mgr.Store.UpdateMeta(f.decoder.ID, func(m *subagent.Meta) { m.Resumable = false }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Post(f.ctx, PostInput{Sender: lead, Mentions: []string{"@children"}, Text: "x"}); err == nil || !strings.Contains(err.Error(), "resolved to nobody") {
		t.Fatalf("empty group err = %v", err)
	}
}

// A group post is stored once and wakes every open member; each frame marks
// its reader and names the group.
func TestGroupPostStoredOnceDeliveredToEach(t *testing.T) {
	// A lead and two children are all this test needs. Neither child is steered,
	// so the delivered post carries the mock's no-hold directive.
	e := newTestEnv(t)
	f := &fixture{testEnv: e, a: e.room()}
	f.lead = e.start(f.a, "lead", nil)
	f.decoder = e.start(f.a, "decoder", &f.lead)
	f.parser = e.start(f.a, "parser", &f.lead)
	res, err := f.svc.Post(f.ctx, PostInput{Sender: f.session(f.lead), Mentions: []string{"@children"}, Text: "SLEEP:0\nInterface changed in abc123. Rebase."})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := f.svc.Rooms.Messages(f.a.ID)
	if len(msgs) != 1 || msgs[0].ID != res.Message.ID || !reflect.DeepEqual(msgs[0].Via, []string{"@children"}) {
		t.Fatalf("stored = %#v", msgs)
	}
	if len(res.Deliveries) != 2 {
		t.Fatalf("deliveries = %#v", res.Deliveries)
	}
	for _, d := range res.Deliveries {
		if d.Delivery != "session-queued" || d.Turn != 0 {
			t.Fatalf("delivery = %#v", d)
		}
	}
	for _, info := range []subagent.Info{f.decoder, f.parser} {
		f.wait(info.ID, 2)
		turn, err := f.mgr.Store.ReadTurn(info.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		want := "To: @children = "
		if !strings.Contains(turn.Prompt, "[dyna message "+res.Message.ID+" from "+f.a.Name+":lead]") || !strings.Contains(turn.Prompt, want) || !strings.Contains(turn.Prompt, info.Address()+" (you)") || !strings.Contains(turn.Prompt, "Reply: dyna post @"+f.a.Name+":lead") {
			t.Fatalf("%s prompt = %q", info.Name, turn.Prompt)
		}
	}
}

// After the post is stored, delivery is best effort: a failed recipient is
// reported and the error wraps ErrDelivery.
func TestPostReportsDeliveryFailureAfterStoring(t *testing.T) {
	f := newFixture(t)
	run := f.newRun("wf_finished", f.a, nil)
	run.Finish("ok", "null", nil)
	res, err := f.svc.Post(f.ctx, PostInput{
		Sender: Sender{Kind: SenderOwner, Room: f.a.Name}, Mentions: []string{"@root", "@run:" + run.Meta.ID + "/agent:1"}, Text: "late",
	})
	if !errors.Is(err, ErrDelivery) {
		t.Fatalf("err = %v", err)
	}
	if res.Message.ID == "" || len(res.Deliveries) != 2 || res.Deliveries[0].Delivery != "room" || res.Deliveries[1].Delivery != "failed" || res.Deliveries[1].Error == "" {
		t.Fatalf("result = %#v", res)
	}
	if msgs, _ := f.svc.Rooms.Messages(f.a.ID); len(msgs) != 1 {
		t.Fatalf("stored = %#v", msgs)
	}
}

func TestWorkerSteeringUsesBoundedPointerFrame(t *testing.T) {
	f := newTestEnv(t)
	rec := f.room()
	run := f.newRun("wf_steer", rec, nil)
	if _, err := run.StartAgentJournal(3, "worker", "test", "", "task"); err != nil {
		t.Fatal(err)
	}
	if err := runstore.ActivateAgentSteering(run.Meta.ID, 3); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("long steering text ", 400)
	res, err := f.svc.Post(f.ctx, PostInput{Sender: Sender{Kind: SenderOwner}, Mentions: []string{"@run:" + run.Meta.ID + "/agent:3"}, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Deliveries[0]; d.Delivery != "worker-steered" || !d.Pointer {
		t.Fatalf("delivery = %#v", d)
	}
	steered, _, err := runstore.ReadAgentSteeringFrom(run.Meta.ID, 3, 0, false)
	if err != nil || len(steered) != 1 {
		t.Fatalf("steering = %#v, %v", steered, err)
	}
	got := steered[0].Message
	if len(got) > runstore.MaxSteeringMessageBytes || !strings.Contains(got, "Full text: dyna room read "+rec.Name+" "+res.Message.ID+"]") {
		t.Fatalf("frame (%d bytes) = %q", len(got), got)
	}
	if res.Message.Text != strings.TrimSpace(text) {
		t.Fatal("the stored post lost its full text")
	}
}

func TestSessionCompletionsGoOnlyToOwner(t *testing.T) {
	f := newFixture(t)
	top, err := f.svc.CompleteShell(f.ctx, ShellExit{ID: "42", RoomID: f.a.ID, Code: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top.Message.To, []string{f.a.Name}) || top.Message.From != "shell:42" || top.Message.Kind != "failed" || top.Deliveries[0].Delivery != "room" {
		t.Fatalf("top-level completion = %#v", top)
	}
	child, err := f.svc.CompleteSessionTurn(f.ctx, f.decoder.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child.Message.To, []string{f.lead.Address()}) || child.Message.Turn != 1 || !reflect.DeepEqual(child.Message.Via, []string{"@parent"}) || len(child.Deliveries) != 1 || child.Deliveries[0].Delivery != "session-queued" {
		t.Fatalf("child completion = %#v", child)
	}
	f.assertIdleTurns(f.lead, 1)
	if _, err := f.svc.CompleteShell(f.ctx, ShellExit{ID: "43"}); !errors.Is(err, errNoRoom) {
		t.Fatalf("completion without room err = %v", err)
	}
}

// assertIdleTurns checks that completion delivery did not start a turn.
func (f *fixture) assertIdleTurns(info subagent.Info, turns int) {
	f.t.Helper()
	got, err := f.mgr.Store.Info(info.ID)
	if err != nil || got.Turns != turns || got.Status != subagent.StatusIdle {
		f.t.Fatalf("session %s = %+v, %v; want idle with %d turns", info.Name, got, err, turns)
	}
}

// finish waits for turn n of info and runs the turn-end report, as the
// detached turn runner does.
func (f *fixture) finish(info subagent.Info, n int) Result {
	f.t.Helper()
	f.wait(info.ID, n)
	res, err := f.svc.CompleteSessionTurn(f.ctx, info.ID, n)
	if err != nil {
		f.t.Fatalf("CompleteSessionTurn(%s, %d): %v", info.Name, n, err)
	}
	return res
}

func (f *fixture) postsSince(rec room.Record, offset int64) []room.Message {
	f.t.Helper()
	msgs, _, err := f.svc.Rooms.ReadFrom(rec.ID, offset)
	if err != nil {
		f.t.Fatal(err)
	}
	return msgs
}

// completedReportSpawner supplies terminal setup records without running a
// harness. Report-policy tests still use real session admission and stores.
type completedReportSpawner struct{}

func (completedReportSpawner) Spawn(m *subagent.Manager, id string, n int, lock *os.File) error {
	defer lock.Close()
	if _, err := m.Store.UpdateTurn(id, n, func(turn *subagent.Turn) {
		turn.Status, turn.Ended = subagent.TurnOK, time.Now()
		turn.HarnessSession = "mock-" + id
	}); err != nil {
		return err
	}
	_, err := m.Store.UpdateMeta(id, func(meta *subagent.Meta) {
		meta.Status, meta.HarnessSession = subagent.StatusIdle, "mock-"+id
	})
	return err
}

// Failed and stopped turns report once, with or without an obligation.
// Concrete errors alert the parent. Claiming preserves the owner UI output.
func TestTerminalSessionReportsPreserveOutputAndAlertOnlyFailures(t *testing.T) {
	for _, status := range []string{subagent.TurnError, subagent.TurnStopped} {
		for _, owed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/owed=%t", status, owed), func(t *testing.T) {
				e := newTestEnv(t, func(e *testEnv) {
					e.mgr.Spawner = completedReportSpawner{}
					e.waitForTurn = nil
				})
				f := &fixture{testEnv: e, a: e.room()}
				f.lead = e.start(f.a, "lead", nil)
				f.decoder = e.start(f.a, "decoder", &f.lead)
				_, err := f.mgr.Store.UpdateTurn(f.decoder.ID, 1, func(turn *subagent.Turn) {
					turn.Status, turn.Result, turn.Error = status, "partial output", "failure detail"
					if !owed {
						turn.ReportTo = nil
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				report, err := f.svc.CompleteSessionTurn(f.ctx, f.decoder.ID, 1)
				if err != nil {
					t.Fatal(err)
				}
				if report.Message.Kind != "failed" || !reflect.DeepEqual(report.Message.To, []string{f.lead.Address(), f.a.Name}) || !reflect.DeepEqual(report.Message.Via, []string{"@parent", "@root"}) || len(report.Deliveries) != 2 || report.Deliveries[0].Delivery != "session-queued" {
					t.Fatalf("terminal report = %#v", report)
				}
				if !strings.Contains(report.Message.Text, "failure detail") || !strings.Contains(report.Message.Text, "dyna subagent output "+f.decoder.Address()) {
					t.Fatalf("terminal report lost the error or output link: %q", report.Message.Text)
				}
				if again, err := f.svc.CompleteSessionTurn(f.ctx, f.decoder.ID, 1); err != nil || again.Message.ID != "" {
					t.Fatalf("duplicate terminal report = %#v, %v", again, err)
				}
				f.assertIdleTurns(f.lead, 1)
				f.assertIdleTurns(f.decoder, 1)
				turn, err := f.mgr.Store.ReadTurn(f.decoder.ID, 1)
				if err != nil || turn.Status != status || turn.Result != "partial output" || turn.Error != "failure detail" || !turn.Reported {
					t.Fatalf("stored turn = %+v, %v", turn, err)
				}
				queue, err := f.mgr.Store.SessionQueue(f.svc.Rooms, f.lead.ID)
				if err != nil || len(queue) != 1 || queue[0].ID != report.Message.ID {
					t.Fatalf("parent report queue = %+v, %v", queue, err)
				}
			})
		}
	}
}

// A runner that died mid-turn is repaired on the next read and reported as
// failed exactly once, however many readers see it.
func TestRepairedTurnReportsFailedOnce(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	f := &fixture{testEnv: e, a: e.room()}
	f.lead = e.start(f.a, "lead", nil)
	f.decoder = e.start(f.a, "decoder", &f.lead)
	if f.mgr.Store.ReportRepaired == nil {
		t.Fatal("notify did not wire the repair reporter")
	}
	start, _ := f.svc.Rooms.FileSize(f.a.ID)
	// Simulate a runner killed before its report: the record says running,
	// and no turn lock is held. A reported turn is already final.
	if _, err := f.mgr.Store.UpdateTurn(f.decoder.ID, 1, func(tu *subagent.Turn) {
		tu.Status, tu.Reported = subagent.TurnRunning, false
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.mgr.Store.Info(f.decoder.ID); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := f.mgr.Store.ReadTurn(f.decoder.ID, 1)
	if err != nil || turn.Status != subagent.TurnError || !turn.Reported {
		t.Fatalf("repaired turn = %+v, %v; want one finalized failure", turn, err)
	}
	posts := f.postsSince(f.a, start)
	if len(posts) != 1 || posts[0].Kind != "failed" || !reflect.DeepEqual(posts[0].To, []string{f.lead.Address(), f.a.Name}) || !strings.Contains(posts[0].Text, "turn runner exited") {
		t.Fatalf("repair posts = %#v", posts)
	}
	f.wait(f.lead.ID, 2)
}

func TestRetryDeliveredPostDoesNotStartEmptyFollowup(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	agent := e.start(rec, "worker", nil)
	input := PostInput{ID: "msg_0123456789abcdef", Sender: Sender{Kind: SenderOwner, Room: rec.Name}, Mentions: []string{"@" + agent.Address()}, Text: "RESPOND: one durable message"}
	first, err := e.svc.Post(e.ctx, input)
	if err != nil || len(first.Deliveries) != 1 || first.Deliveries[0].Turn != 0 {
		t.Fatalf("first post = %+v, %v", first, err)
	}
	e.wait(agent.ID, 2)
	retry, err := e.svc.Post(e.ctx, input)
	if err != nil || len(retry.Deliveries) != 1 || retry.Message.ID != first.Message.ID || retry.Deliveries[0].Turn != 0 || retry.Deliveries[0].Delivery != "session-queued" {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	state, err := e.mgr.Store.Info(agent.ID)
	if err != nil || state.Turns != 2 || state.Status != subagent.StatusIdle {
		t.Fatalf("retry created an empty turn: %+v, %v", state, err)
	}
	delivered, err := e.mgr.Store.ReadTurn(agent.ID, 2)
	if err != nil || len(delivered.Steering) != 1 || !delivered.Steering[0].Delivered || delivered.Steering[0].ID != input.ID {
		t.Fatalf("post was not delivered exactly once: %+v, %v", delivered, err)
	}
}

func TestSessionCompletionSpillsFullReport(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("界", 1700)
	_, err := f.mgr.Store.UpdateTurn(f.decoder.ID, 1, func(turn *subagent.Turn) {
		turn.Result = long
	})
	if err != nil {
		t.Fatal(err)
	}
	report := f.finish(f.decoder, 1)
	dir, err := f.mgr.Store.Dir(f.decoder.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "parent-output", "turn-1.txt")
	full, err := os.ReadFile(path)
	want := CompletionText(f.decoder.Address(), 1, "completed", long, "")
	if err != nil || string(full) != want || len([]rune(report.Message.Text)) > 1500 || !strings.Contains(report.Message.Text, path) {
		t.Fatalf("completion = %s, full length = %d, error = %v", report.Message.Text, len(full), err)
	}
	turn, err := f.mgr.Store.ReadTurn(f.decoder.ID, 1)
	if err != nil || turn.Result != long {
		t.Fatalf("stored result changed: %v", err)
	}
}

func TestSessionCompletionSpillFailurePostsFallbackAndRetryIsSafe(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	child := e.start(rec, "child", nil)
	long := strings.Repeat("界", 1700)
	if _, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) { turn.Result = long }); err != nil {
		t.Fatal(err)
	}
	dir, err := e.mgr.Store.Dir(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "parent-output")
	// A regular file makes the spill fail even when tests run as root.
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	pointer := fmt.Sprintf("dyna subagent output %s --turn 1", child.ID)
	if err != nil || result.Message.ID == "" || len([]rune(result.Message.Text)) > 1500 ||
		!strings.Contains(result.Message.Text, "Could not save full report") || !strings.Contains(result.Message.Text, pointer) {
		t.Fatalf("fallback = %+v, error = %v", result.Message, err)
	}
	stored, err := e.svc.Rooms.Message(rec.ID, result.Message.ID)
	if err != nil || stored.Text != result.Message.Text {
		t.Fatalf("fallback missing from room: %v", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	retry, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil || retry.Message.ID != "" {
		t.Fatalf("retry duplicated fallback: %+v, %v", retry, err)
	}
	turn, err := e.mgr.Store.ReadTurn(child.ID, 1)
	if err != nil || turn.Result != long || !turn.Reported {
		t.Fatalf("stored turn = %+v, error = %v", turn, err)
	}
}

// A forward stores a snapshot of a post of the recipients' room and reaches
// the recipient's frame with the original text, author and time. A missing
// post stores nothing. The original keeps its kind, so every kind forwards.
func TestPostForwardsAnyKindAndRejectsUnknownPost(t *testing.T) {
	f := newFixture(t)
	for _, kind := range []string{"", "completed", "notice"} {
		orig, err := f.svc.Rooms.Post(f.a.ID, room.PostInput{From: f.a.Name + ":decoder", Kind: kind, To: []string{f.a.Name}, Text: "original " + kind})
		if err != nil {
			t.Fatal(err)
		}
		res, err := f.svc.Post(f.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: f.a.Name, Human: true}, Mentions: []string{"@root"}, ForwardOf: orig.ID, Text: "see this"})
		if err != nil {
			t.Fatal(kind, err)
		}
		fw := res.Message.Forward
		if fw == nil || fw.ID != orig.ID || fw.Kind != orig.Kind || fw.RoomID != f.a.ID || fw.Text != orig.Text || fw.From != orig.From {
			t.Fatalf("%q: forward = %#v", kind, fw)
		}
		if frame := res.Message.Frame(room.FrameOptions{Room: f.a.Name}); !strings.Contains(frame, "originally written by "+orig.From) || !strings.Contains(frame, "> "+orig.Text) {
			t.Fatalf("%q: frame:\n%s", kind, frame)
		}
	}
	before, _ := f.svc.Rooms.Messages(f.a.ID)
	_, err := f.svc.Post(f.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: f.a.Name}, Mentions: []string{"@root"}, ForwardOf: "msg_missing"})
	if err == nil || !strings.Contains(err.Error(), "cannot forward") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := f.svc.Rooms.Messages(f.a.ID); len(after) != len(before) {
		t.Fatalf("a refused forward stored %d posts", len(after)-len(before))
	}
}

func TestPostOnlyStoresInputUntilEngineRuns(t *testing.T) {
	f := newFixture(t)
	f.mgr.Spawner = questionUnavailableSpawner{}
	res, err := f.svc.Post(f.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: f.a.Name}, Mentions: []string{"@decoder"}, Text: "RESPOND: queued"})
	if err != nil {
		t.Fatalf("poster tried to launch a turn: %v", err)
	}
	if len(res.Deliveries) != 1 || res.Deliveries[0].Delivery != "session-queued" || res.Deliveries[0].Turn != 0 {
		t.Fatalf("receipt: %+v", res.Deliveries)
	}
	meta, _ := f.mgr.Store.ReadMeta(f.decoder.ID)
	if meta.Turns != 1 {
		t.Fatalf("poster allocated turn %d", meta.Turns)
	}
	if !res.Message.Wake {
		t.Fatal("post did not persist its wake intent")
	}
}

func TestQueuedIntegrationsKeepExactSessionWakeTargets(t *testing.T) {
	f := newFixture(t, withImmediateTurns)
	completion, err := f.svc.CompleteShell(f.ctx, ShellExit{ID: "exact", RoomID: f.a.ID, Parent: f.lead.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !completion.Message.Wake || completion.Message.SessionIDs[f.lead.Address()] != f.lead.ID {
		t.Fatalf("completion lost target identity: %+v", completion.Message)
	}
	q, err := f.svc.QuestionStore().Ask(questions.Question{Room: f.a.ID, Asker: f.decoder.ID, To: "owner", Text: "Continue?", Meanwhile: "test", Options: []string{"yes", "no"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AnswerQuestion(f.ctx, Sender{Kind: SenderOwner, Room: f.a.Name}, "", q.ID, "yes", ""); err != nil {
		t.Fatal(err)
	}
	messages, _ := f.svc.Rooms.Messages(f.a.ID)
	found := false
	for _, msg := range messages {
		if strings.HasPrefix(msg.Text, "[ANSWER ") {
			found = true
			if !msg.Wake || msg.SessionIDs[f.decoder.Address()] != f.decoder.ID {
				t.Fatalf("answer lost target identity: %+v", msg)
			}
		}
	}
	if !found {
		t.Fatal("answer was not saved")
	}
}

// After adopt, @parent names the new parent, not the old one.
func TestParentFollowsAdopt(t *testing.T) {
	f := newFixture(t)
	if _, err := f.mgr.Adopt(context.Background(), f.decoder.ID, f.crc.ID); err != nil {
		t.Fatal(err)
	}
	moved, err := f.mgr.Store.Info(f.decoder.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.resolve(f.session(moved), []string{"@parent"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{f.a.Name + ":crc"}; !reflect.DeepEqual(got.addrs(), want) {
		t.Fatalf("@parent = %v, want %v", got.addrs(), want)
	}
}
