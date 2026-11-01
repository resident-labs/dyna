// Package live owns transient engine state and its notifications.
package live

import (
	"sync"

	"github.com/resident-labs/dyna/contract"
)

// Live holds live state. Activity publication takes activityMu, then Session.mu,
// then mu. Publish runs under Session.mu and uses non-blocking sends. Code under
// mu takes no other lock and sends no channel messages. wakeMu never overlaps mu.
type Live struct {
	mu            sync.Mutex
	activityMu    sync.Mutex
	sessions      map[contract.SessionID]*Session
	closed        map[contract.SessionID]struct{}
	activityCount uint64
	subscribers   map[*Subscription]contract.RoomID
	wakeMu        sync.Mutex
	wakePending   map[contract.SessionID]bool
	wakeSignal    chan struct{}
	deliver       func(map[contract.SessionID]bool)
}

// Session is a stable handle. Its mutex guards all its fields.
type Session struct {
	mu            sync.Mutex
	live          *Live
	id            contract.SessionID
	removed       bool
	activity      contract.TurnActivity
	activityOrder uint64
}

// Subscription uses mu to serialize sends with close.
type Subscription struct {
	mu     sync.Mutex
	ch     chan contract.Event
	closed bool
}

func New(deliver func(map[contract.SessionID]bool)) *Live {
	return &Live{sessions: make(map[contract.SessionID]*Session), closed: make(map[contract.SessionID]struct{}), subscribers: make(map[*Subscription]contract.RoomID), wakePending: make(map[contract.SessionID]bool), wakeSignal: make(chan struct{}, 1), deliver: deliver}
}

func (l *Live) Subscribe(room contract.RoomID, capacity int) (*Subscription, chan contract.Event) {
	sub := &Subscription{ch: make(chan contract.Event, capacity)}
	l.mu.Lock()
	l.subscribers[sub] = room
	l.mu.Unlock()
	return sub, sub.ch
}

func (l *Live) Unsubscribe(sub *Subscription) {
	l.mu.Lock()
	delete(l.subscribers, sub)
	l.mu.Unlock()
}

// Publish copies subscribers under mu and sends outside it. Owner-only transcript
// notifications close a full subscriber so it reconnects and catches up.
func (l *Live) Publish(room contract.RoomID, event contract.Event, ownerOnly bool) {
	l.mu.Lock()
	var targets []*Subscription
	for sub, target := range l.subscribers {
		if target == "" || !ownerOnly && target == room {
			targets = append(targets, sub)
		}
	}
	l.mu.Unlock()
	for _, sub := range targets {
		sub.mu.Lock()
		if !sub.closed {
			select {
			case sub.ch <- event:
			default:
				if ownerOnly {
					close(sub.ch)
					sub.closed = true
				}
			}
		}
		closed := sub.closed
		sub.mu.Unlock()
		if closed {
			l.Unsubscribe(sub)
		}
	}
}

// CreateSession accepts only an ID read from an open session by the caller.
func (l *Live) CreateSession(id contract.SessionID) (*Session, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, closed := l.closed[id]; closed {
		return nil, false
	}
	s := l.sessions[id]
	if s == nil {
		s = &Session{live: l, id: id}
		l.sessions[id] = s
	}
	return s, true
}

// LookupSession does not create state.
func (l *Live) LookupSession(id contract.SessionID) (*Session, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.sessions[id]
	return s, ok
}

// CloseSession atomically removes the entry and records its closed ID.
func (l *Live) CloseSession(id contract.SessionID) {
	l.mu.Lock()
	s := l.sessions[id]
	if s == nil {
		l.closed[id] = struct{}{}
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	l.mu.Lock()
	delete(l.sessions, id)
	l.closed[id] = struct{}{}
	s.removed = true
	l.mu.Unlock()
}

// PublishActivity serializes updates and publication so streams see the same order as lists.
func (s *Session) PublishActivity(room contract.RoomID, activity contract.TurnActivity, event contract.Event) bool {
	l := s.live
	l.activityMu.Lock()
	defer l.activityMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed {
		return false
	}
	l.mu.Lock()
	l.activityCount++
	s.activity, s.activityOrder = activity, l.activityCount
	l.mu.Unlock()
	l.Publish(room, event, false)
	return true
}

// Activity returns the last activity and its global order, or zero values.
func (l *Live) Activity(id contract.SessionID) (contract.TurnActivity, uint64) {
	s, ok := l.LookupSession(id)
	if !ok {
		return contract.TurnActivity{}, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed {
		return contract.TurnActivity{}, 0
	}
	return s.activity, s.activityOrder
}

// Wake queues store notifications without taking mu.
func (l *Live) Wake(ids []contract.SessionID, events []contract.Event) {
	l.wakeMu.Lock()
	defer l.wakeMu.Unlock()
	for _, id := range ids {
		if _, ok := l.wakePending[id]; !ok {
			l.wakePending[id] = false
		}
	}
	for _, event := range events {
		switch body := event.Body.(type) {
		case contract.TurnEnded:
			if _, ok := l.wakePending[body.Session]; !ok {
				l.wakePending[body.Session] = false
			}
		case contract.BoxChanged:
			l.wakePending[body.Session] = l.wakePending[body.Session] || (body.To == contract.BoxClosing && body.Reason != "")
		}
	}
	if len(l.wakePending) != 0 {
		select {
		case l.wakeSignal <- struct{}{}:
		default:
		}
	}
}

// Run drains wakes without holding wakeMu while it delivers them.
func (l *Live) Run(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-l.wakeSignal:
		}
		l.wakeMu.Lock()
		pending := l.wakePending
		l.wakePending = make(map[contract.SessionID]bool)
		l.wakeMu.Unlock()
		l.deliver(pending)
	}
}
