package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
)

// flushEnv is one room pair with a root flusher on a fake clock.
type flushEnv struct {
	f      *rootFlush
	rooms  *room.Store
	clock  *fakeClock
	rec    room.Record
	silent room.Record // a second room that never holds root input
}

func newFlushEnv(t *testing.T) *flushEnv {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rooms := room.DefaultStore()
	t.Cleanup(func() {
		if err := database.Close(filepath.Dir(rooms.Root)); err != nil {
			t.Error(err)
		}
	})
	rec, err := rooms.Create(room.CreateInput{Name: "work", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	silent, err := rooms.Create(room.CreateInput{Name: "idle", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	clock := newFakeClock()
	return &flushEnv{f: newRootFlush(rooms, clock.timers(ctx)), rooms: rooms, clock: clock, rec: rec, silent: silent}
}

// post stores one post and feeds it to the flusher, as the engine's watch loop does.
func (e *flushEnv) post(t *testing.T, in room.PostInput) room.Message {
	t.Helper()
	msg, err := e.rooms.Post(e.rec.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	e.f.events(roomsvc.Event{Kind: "posts", RoomID: e.rec.ID, Posts: []roomsvc.Post{{Message: msg}}})
	return msg
}

// quiet posts passive engine input: a typed note the root seat reads on its
// next turn without a wake.
func (e *flushEnv) quiet(t *testing.T, text string) room.Message {
	t.Helper()
	return e.event(t, room.SessionEvent{Type: room.SessionInstructions, Session: "work:lead", Detail: text}, "work")
}

// wake posts an alert: a typed event that wakes the root seat now.
func (e *flushEnv) wake(t *testing.T, text string) room.Message {
	t.Helper()
	return e.event(t, room.AlertEvent{Source: room.AlertEngine, Summary: text}, "work")
}

// event stores one typed event with a fresh post ID and reports it to the
// flusher as a new post, as the room watch does.
func (e *flushEnv) event(t *testing.T, ev room.Event, to ...string) room.Message {
	t.Helper()
	producer := room.EventProducer("")
	if alert, ok := ev.(room.AlertEvent); ok && alert.Source == room.AlertEngine {
		producer = room.ProducerEngine
	}
	msg, err := e.rooms.PostEventInput(room.PostEventInput{RoomID: e.rec.ID, Event: ev, FreshID: true, Producer: producer, To: to})
	if err != nil {
		t.Fatal(err)
	}
	e.f.events(roomsvc.Event{Kind: "posts", RoomID: e.rec.ID, Posts: []roomsvc.Post{{Message: msg}}})
	return msg
}

// timedFlush returns the room's timed-flush notice, if the engine posted one.
func (e *flushEnv) timedFlush(t *testing.T) (room.Message, bool) {
	t.Helper()
	msgs, err := e.rooms.Messages(e.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range msgs {
		if msg.From == "dyna" && strings.HasPrefix(msg.Text, "[TIMED FLUSH]") {
			return msg, true
		}
	}
	return room.Message{}, false
}

// Quiet root input that no wake releases is delivered as one timed-flush
// notice after the quiet period, and only then. The notice itself wakes the
// root seat, so both the internal root and an external watch read the held
// batch through their existing batching.
func TestRootFlushDeliversHeldPostsAfterTheQuietPeriod(t *testing.T) {
	e := newFlushEnv(t)
	e.quiet(t, "Keep the queue paused")
	e.clock.advance(rootFlushQuiet - time.Second)
	if _, ok := e.timedFlush(t); ok {
		t.Fatal("timed flush arrived before the quiet period ended")
	}
	e.clock.advance(time.Second)
	msg, ok := e.timedFlush(t)
	if !ok {
		t.Fatal("no timed flush after the quiet period")
	}
	if !msg.NeedsRoot || !msg.Wake || msg.Kind != "engine" || msg.Notice.Kind != "root-flush" {
		t.Fatalf("timed flush is not a waking notice: %+v", msg)
	}
	if decision, _ := room.RootContext(msg, e.rec.Name); decision != room.RootWake {
		t.Fatalf("timed flush must wake the root seat: %+v", msg)
	}
	if msgs, err := e.rooms.Messages(e.silent.ID); err != nil || len(msgs) != 0 {
		t.Fatalf("the idle room received posts: %v, %v", msgs, err)
	}
	e.clock.advance(rootFlushQuiet)
	if second, err := e.rooms.Messages(e.rec.ID); err != nil || len(second) != 2 {
		t.Fatalf("the flush must post exactly one notice: %v, %v", second, err)
	}
}

// A waking post delivers the held batch itself, so the timer restarts; only a
// later quiet post arms a new one.
func TestRootFlushRestartsOnEveryDelivery(t *testing.T) {
	e := newFlushEnv(t)
	e.quiet(t, "Keep the queue paused")
	e.wake(t, "Main is red")
	e.clock.advance(3 * rootFlushQuiet)
	if _, ok := e.timedFlush(t); ok {
		t.Fatal("a wake delivered the held posts, so the timer must not fire")
	}
	e.quiet(t, "Batch stays paused")
	e.clock.advance(rootFlushQuiet)
	if _, ok := e.timedFlush(t); !ok {
		t.Fatal("the timer did not restart after the delivery")
	}
}

// A stream of quiet posts cannot push the flush away: the first held post
// fixes the deadline.
func TestRootFlushDeadlineFollowsTheFirstHeldPost(t *testing.T) {
	e := newFlushEnv(t)
	e.quiet(t, "first")
	e.clock.advance(10 * time.Minute)
	e.quiet(t, "second")
	e.clock.advance(5 * time.Minute)
	if _, ok := e.timedFlush(t); !ok {
		t.Fatal("a quiet stream pushed the flush past the quiet period")
	}
}

// A room that held quiet root input while the engine was down gets its timer
// back; a room without held input gets none.
func TestRootFlushRecoversHeldInputAfterARestart(t *testing.T) {
	e := newFlushEnv(t)
	e.quiet(t, "Held across the restart")
	e.wake(t, "Main is red")
	// A new flusher starts with an empty arm state, as after an engine restart.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	restarted := newRootFlush(e.rooms, e.clock.timers(ctx))
	restarted.recover()
	e.clock.advance(rootFlushQuiet)
	if _, ok := e.timedFlush(t); ok {
		t.Fatal("a wake had delivered the held posts, so recovery must not arm a timer")
	}
	// The new quiet post goes straight to the store: the old flusher gets no
	// event, so only the restarted flusher's recovery can arm the flush.
	if _, err := e.rooms.PostEventInput(room.PostEventInput{RoomID: e.rec.ID, Event: room.SessionEvent{Type: room.SessionInstructions, Session: "work:lead", Detail: "Held after the restart"}, FreshID: true, To: []string{"work"}}); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(t.Context())
	t.Cleanup(cancel2)
	again := newRootFlush(e.rooms, e.clock.timers(ctx2))
	again.recover()
	e.clock.advance(rootFlushQuiet)
	if _, ok := e.timedFlush(t); !ok {
		t.Fatal("held input from before the restart was never flushed")
	}
}

// Posts between other recipients never arm a timer.
func TestRootFlushIgnoresInputThatIsNotRoots(t *testing.T) {
	e := newFlushEnv(t)
	e.post(t, room.PostInput{Wake: true, From: "work:agent", Kind: "message", To: []string{"work:other"}, Text: "agent to agent"})
	e.clock.advance(2 * rootFlushQuiet)
	if _, ok := e.timedFlush(t); ok {
		t.Fatal("posts to other recipients produced a timed flush")
	}
}

// The owner's post to an agent is quiet root context, not a wake, so it arms
// the flush like every other held post: without it, no post would ever
// release the owner's DMs to an external root that no agent post wakes.
func TestRootFlushArmsOnOwnerDMsToAgents(t *testing.T) {
	e := newFlushEnv(t)
	e.post(t, room.PostInput{Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work:agent"}, Via: []string{"@agent"}, Text: "owner to agent"})
	e.clock.advance(rootFlushQuiet)
	if _, ok := e.timedFlush(t); !ok {
		t.Fatal("a held owner DM never armed the timed flush")
	}
	msgs, err := e.rooms.Messages(e.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("the flush must add one notice to the held DM: %+v", msgs)
	}
}

// A quiet post that lands between engine start and the watch's first scan
// falls inside the scan's post baseline, which is never emitted as an event,
// so only recovery can flush it. startRootFlush recovers once that scan
// reports back; a flusher that recovered before the scan would miss the post.
func TestRootFlushRecoversQuietInputFromBeforeTheFirstScan(t *testing.T) {
	e := newFlushEnv(t)
	svc := roomsvc.New(notify.New(e.rooms, nil))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	_, recovered := startRootFlush(svc, e.clock.timers(ctx))
	// The quiet post lands before the first scan takes the room's baseline.
	if _, err := e.rooms.PostEventInput(room.PostEventInput{RoomID: e.rec.ID, Event: room.SessionEvent{Type: room.SessionInstructions, Session: "work:lead", Detail: "Quiet before the first scan"}, FreshID: true, To: []string{"work"}}); err != nil {
		t.Fatal(err)
	}
	svc.AfterFirstScan()
	select {
	case <-recovered:
	case <-time.After(30 * time.Second):
		t.Fatal("recovery never ran after the first scan")
	}
	// Recovery alone armed the room; only its timer exists on the clock.
	e.clock.advance(rootFlushQuiet)
	if _, ok := e.timedFlush(t); !ok {
		t.Fatal("quiet input from before the first scan was never flushed")
	}
}

// Passive engine input is read on the next real turn. It must neither cancel
// the first quiet post's deadline nor hide that post from recovery.
func TestRootFlushPreservesDeadlineThroughPassiveInput(t *testing.T) {
	for _, kind := range []string{"engine-note", "owner-dm"} {
		for _, recover := range []bool{false, true} {
			name := kind + "/live"
			if recover {
				name = kind + "/recovery"
			}
			t.Run(name, func(t *testing.T) {
				e := newFlushEnv(t)
				if kind == "owner-dm" {
					e.post(t, room.PostInput{Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work:agent"}, Text: "Repair the test"})
				} else {
					e.quiet(t, "Repair the test")
				}
				e.clock.advance(rootFlushQuiet / 2)
				e.quiet(t, "Updated room rules")
				remaining := rootFlushQuiet / 2
				if recover {
					e.f.t.stop(rootFlushKey(e.rec.ID))
					ctx, cancel := context.WithCancel(t.Context())
					t.Cleanup(cancel)
					e.f = newRootFlush(e.rooms, e.clock.timers(ctx))
					e.f.recover()
					remaining = rootFlushQuiet
				}
				e.clock.advance(remaining - time.Second)
				if _, ok := e.timedFlush(t); ok {
					t.Fatal("passive input caused an early flush")
				}
				e.clock.advance(time.Second)
				if _, ok := e.timedFlush(t); !ok {
					t.Fatal("passive input suppressed the quiet-context flush")
				}
			})
		}
	}
}
