package engine

import (
	"fmt"
	"log"
	"sync"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
)

// rootFlushQuiet is how long quiet root input may wait for a waking post
// before the engine delivers it as one timed flush.
const rootFlushQuiet = 15 * time.Minute

// rootFlushKey is the timer key of one room's flush deadline.
func rootFlushKey(roomID string) string { return timerRootFlush + "/" + roomID }

// rootFlush ends the open-ended hold on quiet root context. Both consumers of
// the root seat batch that context until a wake — the internal root reads it
// with its next woken turn, and an external root's room watch prints it with
// the next waking post — so when no wake comes, one timed-flush notice from
// the engine releases the same batch through that existing batching on both
// paths. One timer per room: the first held post after a delivery arms it,
// and every delivery restarts it. Rooms with no held input get no timer.
// What counts as quiet or waking comes from room.RootContext, the one
// classifier both roots render from, so the flush never disagrees with them.
type rootFlush struct {
	rooms *room.Store
	t     *timers

	mu    sync.Mutex
	armed map[string]bool
}

func newRootFlush(rooms *room.Store, t *timers) *rootFlush {
	return &rootFlush{rooms: rooms, t: t, armed: map[string]bool{}}
}

// startRootFlush wires one flusher into the engine's service. The watch's
// first scan establishes every room's post baseline without emitting the
// posts it covers, so recovery runs only after that scan reports back: a
// quiet post that lands between engine start and the baseline still falls
// within recovery's read, and everything after the baseline arrives as a
// posts event. The returned channel closes when recovery has run.
func startRootFlush(svc *roomsvc.Service, t *timers) (*rootFlush, <-chan struct{}) {
	f := newRootFlush(svc.Notify.Rooms, t)
	scanned := make(chan struct{})
	recovered := make(chan struct{})
	var once sync.Once
	svc.AfterFirstScan = func() {
		once.Do(func() { close(scanned) })
	}
	go func() {
		defer close(recovered)
		<-scanned
		f.recover()
	}()
	return f, recovered
}

// events reacts to new posts. A post that wakes the root seat delivered the
// held batch with it, so the room's timer restarts with the next held post;
// the first quiet root post after that arms the timer for the full quiet
// period, so a stream of quiet posts cannot push the flush away. The owner's
// posts to agents are quiet context too, so they arm the flush like the rest.
func (f *rootFlush) events(ev roomsvc.Event) {
	if ev.Kind != "posts" || len(ev.Posts) == 0 {
		return
	}
	rec, err := f.rooms.Resolve(ev.RoomID)
	if err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range ev.Posts {
		decision, _ := room.RootContext(p.Message, rec.Name)
		if decision == room.RootWake {
			if f.armed[ev.RoomID] {
				f.t.stop(rootFlushKey(ev.RoomID))
				delete(f.armed, ev.RoomID)
			}
			continue
		}
		if decision != room.RootQuiet || f.armed[ev.RoomID] {
			continue
		}
		f.armLocked(ev.RoomID)
	}
}

// recover arms rooms that still held quiet root input while the engine was
// down. The full quiet period restarts, so a restart adds no extra wait
// beyond it.
func (f *rootFlush) recover() {
	records, err := f.rooms.List(true)
	if err != nil {
		log.Printf("root flush recovery: %v", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rec := range records {
		held, err := f.held(rec)
		if err != nil {
			log.Printf("root flush recovery of %s: %v", rec.Name, err)
			continue
		}
		if held && !f.armed[rec.ID] {
			f.armLocked(rec.ID)
		}
	}
}

// armLocked sets the room's flush deadline one quiet period out.
func (f *rootFlush) armLocked(roomID string) {
	f.armed[roomID] = true
	f.t.at(rootFlushKey(roomID), f.t.now().Add(rootFlushQuiet), func() { f.fire(roomID) })
}

// fire posts one timed-flush notice that wakes the root seat, so the held
// batch reaches it through the existing batching. No held posts means no
// nudge; the guard also keeps a second engine on the same store quiet once
// the first posted the notice.
func (f *rootFlush) fire(roomID string) {
	f.mu.Lock()
	delete(f.armed, roomID)
	f.mu.Unlock()
	rec, err := f.rooms.Resolve(roomID)
	if err != nil {
		return
	}
	held, err := f.held(rec)
	if err != nil || !held {
		return
	}
	if _, err := f.rooms.Post(roomID, room.PostInput{
		Wake: true, NeedsRoot: true, From: "dyna", Kind: "notice", To: []string{rec.Name},
		Text: fmt.Sprintf("[TIMED FLUSH] No post woke the root seat for %d minutes, so this notice delivers the held quiet posts as one batch. They did not ask for action; act only where one of them needs a root decision. The full posts stay in the room log.", int(rootFlushQuiet.Minutes())),
	}); err != nil {
		log.Printf("root flush of %s: %v", rec.Name, err)
	}
}

// held reports whether the room still holds quiet root context: a quiet
// post newer than every post that woke the root seat. Posts to other
// recipients count for neither side.
func (f *rootFlush) held(rec room.Record) (bool, error) {
	msgs, err := f.rooms.Messages(rec.ID)
	if err != nil {
		return false, err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		switch decision, _ := room.RootContext(msgs[i], rec.Name); decision {
		case room.RootQuiet:
			return true, nil
		case room.RootWake:
			return false, nil
		}
	}
	return false, nil
}
