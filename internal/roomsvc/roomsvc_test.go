package roomsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

var stampKeys = mode.StampKeys()

type env struct {
	t     *testing.T
	svc   *Service
	mgr   *subagent.Manager
	ctx   context.Context
	turns *turnSpawner
}

// turnSpawner runs turns in process, as subagent.InProcessSpawner does, and
// closes a channel when each runner ends. wait selects on it, so it never
// sleeps through Manager.Wait's 150 ms poll.
type turnSpawner struct {
	ctx     context.Context
	runners *sync.WaitGroup
	done    sync.Map // "<session ID>/<turn>" -> chan struct{}
}

func (s *turnSpawner) Spawn(m *subagent.Manager, id string, n int, lock *os.File) error {
	done := make(chan struct{})
	s.done.Store(fmt.Sprintf("%s/%d", id, n), done)
	s.runners.Add(1)
	go func() {
		defer s.runners.Done()
		defer close(done)
		_ = m.RunTurn(s.ctx, id, n, lock)
	}()
	return nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	// Session scratch must be removed with the fixture after its runners stop.
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("DYNA_SCRATCH_ROOT", filepath.Join(t.TempDir(), "scratch"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	for _, key := range stampKeys {
		t.Setenv(key, "")
	}
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	runners := &sync.WaitGroup{}
	turns := &turnSpawner{ctx: ctx, runners: runners}
	mgr := &subagent.Manager{
		Store:    subagent.DefaultStore(),
		Profiles: func() (*profile.Store, error) { return profiles, nil },
		Settings: settings.Defaults,
		Spawner:  turns,
	}
	t.Cleanup(func() {
		cancel()
		runners.Wait()
		if err := database.Close(filepath.Dir(mgr.Store.Root)); err != nil {
			t.Error(err)
		}
	})
	return &env{t: t, svc: New(notify.New(room.DefaultStore(), mgr)), mgr: mgr, ctx: ctx, turns: turns}
}

// Done is first read by Watch after its initial scan. This gives tests an
// event barrier without adding a production hook or using a fixed sleep.
type startedWatchContext struct {
	context.Context
	started chan struct{}
	once    sync.Once
}

func (c *startedWatchContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.started) })
	return c.Context.Done()
}
func startRoomWatch(t *testing.T, e *env) <-chan Event {
	t.Helper()
	base, cancel := context.WithCancel(e.ctx)
	ctx := &startedWatchContext{Context: base, started: make(chan struct{})}
	events, stopped := make(chan Event, 16), make(chan struct{})
	go func() {
		defer close(stopped)
		e.svc.Watch(ctx, 20*time.Millisecond, func(event Event) {
			select {
			case events <- event:
			case <-base.Done():
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("watch did not stop before database cleanup")
		}
	})
	select {
	case <-ctx.started:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not finish its initial scan")
	}
	return events
}

func (e *env) room(name string) room.Record {
	e.t.Helper()
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: name, CreatedBy: "test"})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) as(parent *subagent.Info) {
	for _, key := range stampKeys {
		e.t.Setenv(key, "")
	}
	if parent != nil {
		e.t.Setenv(mode.EnvMode, mode.Subagent)
		e.t.Setenv(mode.EnvSession, parent.ID)
		e.t.Setenv(mode.EnvDepth, "1")
		e.t.Setenv(mode.EnvRoom, parent.Room)
		e.t.Setenv(mode.EnvRoomID, parent.RoomID)
	}
}

func (e *env) start(rec room.Record, name string, parent *subagent.Info) subagent.Info {
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

func (e *env) wait(id string, turn int) subagent.Info {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	if _, err := e.mgr.TickSessions(ctx); err != nil {
		e.t.Fatal(err)
	}
	if done, ok := e.turns.done.Load(fmt.Sprintf("%s/%d", id, turn)); ok {
		select {
		case <-done.(chan struct{}):
		case <-ctx.Done():
			e.t.Fatalf("runner (%s, %d): %v", id, turn, ctx.Err())
		}
	}
	info, _, err := e.mgr.Wait(ctx, id, turn)
	if err != nil {
		e.t.Fatalf("Wait(%s, %d): %v", id, turn, err)
	}
	return info
}

// doneGoal finishes the goal info holds, as a worker does before it finishes.
func (e *env) doneGoal(info subagent.Info) {
	e.t.Helper()
	store := e.svc.goalService().Store
	held, err := store.OpenHeldBy(info.RoomID, info.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, g := range held {
		if _, err := store.Finish(info.RoomID, g.ID, "owner", "Fixture completed", false); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestMembersTreeAndStates(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	e.start(rec, "decoder", &lead)
	e.start(rec, "crc", nil)
	other := e.room("beta")
	e.start(other, "stranger", nil)

	members, err := e.svc.Members("alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range members {
		got = append(got, strings.Repeat(">", m.Depth)+m.Name)
	}
	if want := "alpha lead >decoder crc"; strings.Join(got, " ") != want {
		t.Fatalf("tree = %q, want %q", strings.Join(got, " "), want)
	}
	if !members[0].Owner || members[0].Children != 2 {
		t.Fatalf("owner row = %+v", members[0])
	}
	if members[1].Children != 1 || members[1].Address != "alpha:lead" {
		t.Fatalf("lead row = %+v", members[1])
	}
	for _, m := range members[1:] {
		if m.State != StateDone {
			t.Fatalf("%s state = %q, want done after an ok turn", m.Name, m.State)
		}
	}
}

func TestOwnerCountSkipsClosedSessions(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	e.start(rec, "open", nil)
	gone := e.start(rec, "gone", nil)
	e.doneGoal(gone)
	if _, err := e.svc.ForceCloseSession(e.ctx, "alpha", gone.ID); err != nil {
		t.Fatal(err)
	}
	members, err := e.svc.Members("alpha", true)
	if err != nil || len(members) != 3 || members[0].Children != 1 {
		t.Fatalf("members with closed = %+v, %v", members, err)
	}
}

func TestPostDefaultsToRootAndMarksHuman(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)

	if _, err := e.svc.Post(e.ctx, "alpha", "@lead   ", ""); err == nil {
		t.Fatal("a draft with only mentions must fail")
	}
	root, err := e.svc.Post(e.ctx, "alpha", "@root note for the internal root", "")
	if err != nil {
		t.Fatal(err)
	}
	// The room log keeps it for an external watcher, and the internal root,
	// which holds the seat, starts with it.
	if !root.Message.Human || root.Message.From != "alpha" || len(root.Message.To) != 1 || root.Message.To[0] != "alpha" ||
		len(root.Message.Via) != 1 || root.Message.Via[0] != "@root" || len(root.Deliveries) != 2 || root.Deliveries[0].Delivery != "room" ||
		root.Deliveries[1].To != "alpha:root" || root.Deliveries[1].Delivery != "root-started" {
		t.Fatalf("@root post = %+v deliveries %+v", root.Message, root.Deliveries)
	}
	// A draft with no mention goes to root, so the owner never wakes every
	// top-level agent by accident.
	plain, err := e.svc.Post(e.ctx, "alpha", "RESPOND: no mention", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plain.Message.Via, ",") != "@root" || strings.Join(plain.Message.To, ",") != "alpha" || plain.Sticky != "" {
		t.Fatalf("no-mention post = %+v sticky %q", plain.Message, plain.Sticky)
	}
	e.wait(lead.ID, 1)
	res, err := e.svc.Post(e.ctx, "alpha", "@lead RESPOND: hi", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Message.Human || res.Message.From != "alpha" || len(res.Message.To) != 1 || res.Message.To[0] != lead.Address() {
		t.Fatalf("message = %+v", res.Message)
	}
	feed, err := e.svc.FeedTail("alpha", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var mine *Post
	for i := range feed.Posts {
		if feed.Posts[i].ID == res.Message.ID {
			mine = &feed.Posts[i]
		}
	}
	if mine == nil || !mine.Owner || !mine.Human || mine.Preview != "" {
		t.Fatalf("own post in feed = %+v", mine)
	}
	e.wait(lead.ID, 2) // the @lead post woke a follow-up turn; let it finish
}

func TestWatchEmitsNewPosts(t *testing.T) {
	e := newEnv(t)
	e.room("alpha")
	events := startRoomWatch(t, e)
	if _, err := e.svc.Post(e.ctx, "alpha", "@root hello", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == "posts" && len(ev.Posts) == 1 && ev.Posts[0].Text == "hello" {
				return
			}
		case <-deadline:
			t.Fatal("no posts event")
		}
	}
}

func TestFoldRule(t *testing.T) {
	short := strings.Repeat("line\n", 10)
	if p, _ := Fold(short, false); p != "" {
		t.Fatalf("ten lines must show in full, got preview %q", p)
	}
	long := "first para\nstill first\n\nsecond para\n\nthird para\n" + strings.Repeat("more\n", 10)
	p, hidden := Fold(long, false)
	if p != "first para\nstill first" || hidden == 0 {
		t.Fatalf("preview = %q hidden = %d", p, hidden)
	}
	tall := strings.Repeat("x\n", 20)
	if p, _ := Fold(tall, false); strings.Count(p, "\n")+1 > FoldedLines {
		t.Fatalf("preview has more than %d lines: %q", FoldedLines, p)
	}
	if p, hidden := Fold("done.\n\nDetails follow.", true); p != "done." || hidden == 0 {
		t.Fatalf("completion fold = %q, %d", p, hidden)
	}
	if p, _ := Fold("one paragraph only", true); p != "" {
		t.Fatalf("a one-paragraph completion must show in full, got %q", p)
	}
}

func TestParseDraft(t *testing.T) {
	members := []string{"lead", "crc", "decoder"}
	for _, c := range []struct {
		draft, text string
		mentions    []string
		def         bool
	}{
		{"hello all", "hello all", []string{"@root"}, true},
		{"@decoder, @crc: fix it", "fix it", []string{"@decoder", "@crc"}, false},
		{"@decoder fix it", "fix it", []string{"@decoder"}, false},
		{"@a -- @literal", "@literal", []string{"@a"}, false},
		{"ask @decoder, then @team.", "ask @decoder, then @team.", []string{"@decoder", "@team"}, false},
		{"thanks (@alpha:lead)", "thanks (@alpha:lead)", []string{"@alpha:lead"}, false},
		{"mail a@b.com, see x/@decoder and @nobody", "mail a@b.com, see x/@decoder and @nobody", []string{"@root"}, true},
		{"run `@decoder` and \\@crc", "run `@decoder` and @crc", []string{"@root"}, true},
		{"@root note", "note", []string{"@root"}, false},
		{"@alpha note", "note", []string{"@alpha"}, false},
	} {
		d := ParseDraft(c.draft, "alpha", members)
		if d.Text != c.text || strings.Join(d.Mentions, ",") != strings.Join(c.mentions, ",") ||
			d.Default != c.def {
			t.Errorf("ParseDraft(%q) = %+v", c.draft, d)
		}
	}
}

func TestTranscriptReadsTurns(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Member.Name != "lead" || len(sess.Turns) != 1 || len(sess.Turns[0].Items) == 0 {
		t.Fatalf("session = %+v", sess)
	}
	if sess.Turns[0].Items[0].Kind != "prompt" {
		t.Fatalf("first item = %+v", sess.Turns[0].Items[0])
	}
}

func TestRoomsOrderUnreadFirst(t *testing.T) {
	e := newEnv(t)
	e.room("quiet")
	busy := e.room("busy")
	e.start(busy, "lead", nil)
	if _, err := e.svc.Notify.Rooms.Post(busy.ID, room.PostInput{From: "busy:lead", To: []string{"busy"}, Text: "for you"}); err != nil {
		t.Fatal(err)
	}
	rooms, err := e.svc.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rooms {
		names = append(names, r.Name)
	}
	if strings.Join(names, " ") != "busy quiet" {
		t.Fatalf("order = %v", names)
	}
	if rooms[0].Unread == 0 || rooms[0].Members != 1 {
		t.Fatalf("busy = %+v", rooms[0])
	}
}

func TestRoomsReportPausedSessions(t *testing.T) {
	e := newEnv(t)
	e.room("quiet")
	held := e.room("held")
	if _, err := e.svc.Notify.Rooms.SetSessionsPaused(context.Background(), held.ID, true); err != nil {
		t.Fatal(err)
	}
	rooms, err := e.svc.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if r.Paused != (r.ID == held.ID) {
			t.Fatalf("%s paused = %v", r.Name, r.Paused)
		}
	}
}

func TestSpeakerLabel(t *testing.T) {
	for _, c := range []struct {
		m    room.Message
		want string
	}{
		{room.Message{From: "alpha", Human: true}, "you"},
		{room.Message{From: "alpha"}, "root"},
		{room.Message{From: "alpha:lead"}, "lead"},
		{room.Message{From: "run:abc"}, "run:abc"},
	} {
		if got := SpeakerLabel(c.m, "alpha"); got != c.want {
			t.Errorf("SpeakerLabel(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestPinsReturnPostsInPinOrder(t *testing.T) {
	e := newEnv(t)
	e.room("alpha")
	var ids []string
	for _, text := range []string{"one", "two", "three"} {
		res, err := e.svc.Post(e.ctx, "alpha", "@root "+text, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.Message.ID)
	}
	if err := e.svc.Pin("alpha", ids[0], ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Pin("alpha", ids[2], ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Pin("alpha", ids[0], ""); err != nil {
		t.Fatalf("repin: %v", err)
	}
	if err := e.svc.Pin("alpha", "msg_nope", ""); err == nil {
		t.Fatal("pinned an unknown post")
	}
	pins, err := e.svc.Pins("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[0].ID != ids[2] || pins[1].ID != ids[0] {
		t.Fatalf("pins = %+v, want three then one", pins)
	}
	for _, p := range pins {
		if !p.Pinned || p.PinnedAt == 0 || p.PinnedBy != "alpha" || p.Speaker != "you" {
			t.Fatalf("pin post = %+v", p)
		}
	}
	feed, err := e.svc.FeedTail("alpha", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range feed.Posts {
		if want := i != 1; p.Pinned != want {
			t.Fatalf("feed post %d (%s) pinned = %v, want %v", i, p.Text, p.Pinned, want)
		}
	}
	if err := e.svc.Unpin(t.Context(), "alpha", ids[2]); err != nil {
		t.Fatal(err)
	}
	if pins, _ = e.svc.Pins("alpha"); len(pins) != 1 || pins[0].ID != ids[0] {
		t.Fatalf("pins after unpin = %+v", pins)
	}
}

func TestWatchEmitsPinChanges(t *testing.T) {
	e := newEnv(t)
	e.room("alpha")
	res, err := e.svc.Post(e.ctx, "alpha", "@root keep this", "")
	if err != nil {
		t.Fatal(err)
	}
	events := startRoomWatch(t, e)
	wait := func(what string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Kind == "pins" && ev.RoomID != "" {
					return
				}
			case <-deadline:
				t.Fatalf("no pins event after %s", what)
			}
		}
	}
	if err := e.svc.Pin("alpha", res.Message.ID, ""); err != nil {
		t.Fatal(err)
	}
	wait("pin")
	if err := e.svc.Unpin(t.Context(), "alpha", res.Message.ID); err != nil {
		t.Fatal(err)
	}
	wait("unpin")
}

func TestProfileChoicesIncludeEffectiveModels(t *testing.T) {
	e := newEnv(t)
	store := &profile.Store{Profiles: []profile.Profile{
		{Name: "chain", Model: "first", Models: []string{"first", "second"}},
		{Name: "legacy", Model: "legacy-model"},
		{Name: "hidden", Disabled: true, Models: []string{"hidden-model"}},
	}}
	e.mgr.Profiles = func() (*profile.Store, error) { return store, nil }
	choices, err := e.svc.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 || strings.Join(choices[0].Models, ",") != "first,second" || strings.Join(choices[1].Models, ",") != "legacy-model" {
		t.Fatalf("choices: %+v", choices)
	}
	choices[0].Models[0] = "changed"
	if store.Profiles[0].Models[0] != "first" {
		t.Fatal("choice shares stored model list")
	}
}

func TestRoomFixtureRemovesScratch(t *testing.T) {
	var root string
	t.Run("fixture", func(fixture *testing.T) {
		newEnv(fixture)
		root = scratch.Root()
		// Refuse to remove a root that this test did not create.
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			fixture.Fatalf("scratch root already exists: %s (%v)", root, err)
		}
		if _, err := scratch.Ensure("fixture-session"); err != nil {
			fixture.Fatal(err)
		}
		// Also remove our root if the regression fails.
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	})
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("fixture left scratch root %s: %v", root, err)
	}
}
