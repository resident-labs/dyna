// Package hub is the engine's change bus. Writers publish one typed Event
// per committed change, and every subscriber gets it. The web server only
// encodes events for its streams; the hub never encodes an event.
package hub

import (
	"strconv"
	"strings"
	"sync"
)

const (
	// Buffer bounds what one slow stream may hold. A full buffer closes
	// that stream; the client reconnects and reloads.
	Buffer = 64
	// Keep is how many recent events a caller of Since can still fetch. A
	// caller further behind is told to reset.
	Keep = 256
)

// Event is one change. Publish sets Epoch and Rev.
type Event struct {
	Topic string // what changed, for example "posts" or "computer"
	Room  string // the room ID, when the change belongs to one
	Key   string // the changed object within the topic, when known
	Epoch int64  // the engine start in Unix milliseconds
	Rev   uint64 // the event's place in its epoch, from 1
	// Body is the payload that web clients read, for example a
	// roomsvc.Event with new posts. Nil when the topic and room say all.
	// It is not typed because the hub must not depend on its publishers.
	// Go subscribers decide on Topic, Room and Key only, and never
	// type-assert Body.
	Body any
}

// ID is the event's stream ID, "epoch:rev". A client sends it back to
// resume after a reconnect.
func (e Event) ID() string {
	return strconv.FormatInt(e.Epoch, 10) + ":" + strconv.FormatUint(e.Rev, 10)
}

// ParseID reads an ID that Event.ID made.
func ParseID(id string) (epoch int64, rev uint64, ok bool) {
	e, r, found := strings.Cut(id, ":")
	if !found {
		return 0, 0, false
	}
	epoch, err := strconv.ParseInt(e, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	rev, err = strconv.ParseUint(r, 10, 64)
	return epoch, rev, err == nil
}

// Hub fans events out to every subscriber. Every subscriber gets every
// event today. Item 15 (a paired device gets only the rooms and scopes it
// was granted) adds that filter where the web server sends events.
//
// It also keeps the last Keep events, for long polls through proxies that
// hold a stream's body until it ends (Cloudflare quick tunnels do).
type Hub struct {
	epoch int64

	mu      sync.Mutex
	subs    map[*sub]struct{}
	rev     uint64
	ring    []Event
	wake    chan struct{}
	dropped int
}

type sub struct {
	ch     chan Event
	closed bool
}

// New makes a hub whose events carry epoch, the engine start in Unix
// milliseconds.
func New(epoch int64) *Hub {
	return &Hub{epoch: epoch, subs: map[*sub]struct{}{}, wake: make(chan struct{})}
}

// Publish numbers ev and sends it to every subscriber. It never blocks: a
// subscriber whose buffer is full is dropped.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rev++
	ev.Epoch, ev.Rev = h.epoch, h.rev
	h.ring = append(h.ring, ev)
	if len(h.ring) > Keep {
		h.ring = append(h.ring[:0:0], h.ring[len(h.ring)-Keep:]...)
	}
	close(h.wake)
	h.wake = make(chan struct{})
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			// Too slow: end the stream so the client resyncs.
			h.drop(s)
			h.dropped++
		}
	}
}

// Since returns the events after rev after of epoch, the newest rev, whether
// the caller must reload instead (reset), and a channel that closes at the
// next Publish. A caller resets when it names another epoch, an engine start
// whose events are gone, or when it fell behind the ring.
func (h *Hub) Since(epoch int64, after uint64) (events []Event, last uint64, reset bool, wake <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if epoch != h.epoch || after > h.rev || (len(h.ring) > 0 && after+1 < h.ring[0].Rev) {
		return nil, h.rev, true, h.wake
	}
	for _, e := range h.ring {
		if e.Rev > after {
			events = append(events, e)
		}
	}
	return events, h.rev, false, h.wake
}

// Epoch is the engine start that this hub's events carry.
func (h *Hub) Epoch() int64 { return h.epoch }

// Rev is the newest event's rev.
func (h *Hub) Rev() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rev
}

// Subscribe returns a channel of every later event, the newest rev before
// them, and a function that ends the subscription. The channel closes when
// the subscriber is dropped as too slow, or when it unsubscribes.
func (h *Hub) Subscribe() (<-chan Event, uint64, func()) {
	s := &sub{ch: make(chan Event, Buffer)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	rev := h.rev
	h.mu.Unlock()
	return s.ch, rev, func() {
		h.mu.Lock()
		h.drop(s)
		h.mu.Unlock()
	}
}

func (h *Hub) drop(s *sub) {
	if !s.closed {
		s.closed = true
		delete(h.subs, s)
		close(s.ch)
	}
}

// Stats is the hub's part of the engine's Health call.
type Stats struct {
	Seq     uint64 `json:"seq"`     // newest event rev
	Streams int    `json:"streams"` // open subscriptions
	MaxLag  int    `json:"maxLag"`  // most events waiting in one subscription
	Dropped int    `json:"dropped"` // subscriptions ended for being too slow
}

// Stats reports the rev, subscriber lag and dropped subscribers.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := Stats{Seq: h.rev, Streams: len(h.subs), Dropped: h.dropped}
	for s := range h.subs {
		st.MaxLag = max(st.MaxLag, len(s.ch))
	}
	return st
}
