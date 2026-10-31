// Package live owns transient engine state and its notifications.
package live

import (
	"sync"

	"github.com/resident-labs/dyna/contract"
)

// Live holds live state. Service.mu, computerLock, readMu, transcriptMu and
// activityMu may precede mu; mu is innermost. Code under mu takes no other lock
// and sends no channel messages. wakeMu is never held with mu.
type Live struct {
	mu           sync.Mutex
	activityMu   sync.Mutex
	activityLast map[contract.SessionID]uint64
	subscribers  map[*Subscription]contract.RoomID
	wakeMu       sync.Mutex
	wakePending  map[contract.SessionID]bool
	wakeSignal   chan struct{}
	deliver      func(map[contract.SessionID]bool)
}

// Subscription uses mu to serialize sends with close.
type Subscription struct {
	mu     sync.Mutex
	ch     chan contract.Event
	closed bool
}

func New(deliver func(map[contract.SessionID]bool)) *Live {
	return &Live{activityLast: make(map[contract.SessionID]uint64), subscribers: make(map[*Subscription]contract.RoomID), wakePending: make(map[contract.SessionID]bool), wakeSignal: make(chan struct{}, 1), deliver: deliver}
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

// PublishActivity drops an older update if a newer update reached publication first.
func (l *Live) PublishActivity(room contract.RoomID, session contract.SessionID, order uint64, event contract.Event) {
	l.activityMu.Lock()
	defer l.activityMu.Unlock()
	if order <= l.activityLast[session] {
		return
	}
	l.activityLast[session] = order
	l.Publish(room, event, false)
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
