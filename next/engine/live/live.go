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
	mu            sync.Mutex
	activityMu    sync.Mutex
	sessions      map[contract.SessionID]sessionState
	activityCount uint64
	subscribers   map[*Subscription]contract.RoomID
	wakeMu        sync.Mutex
	wakePending   map[contract.SessionID]bool
	wakeSignal    chan struct{}
	deliver       func(map[contract.SessionID]bool)
}

type sessionState struct {
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
	return &Live{sessions: make(map[contract.SessionID]sessionState), subscribers: make(map[*Subscription]contract.RoomID), wakePending: make(map[contract.SessionID]bool), wakeSignal: make(chan struct{}, 1), deliver: deliver}
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

// Publish copies subscribers under mu and sends outside it. A full subscriber closes
// for every event it cannot take, so it reconnects and catches up from the store.
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
				close(sub.ch)
				sub.closed = true
			}
		}
		closed := sub.closed
		sub.mu.Unlock()
		if closed {
			l.Unsubscribe(sub)
		}
	}
}

// PublishActivity serializes updates and publication so streams see the same order as lists.
func (l *Live) PublishActivity(room contract.RoomID, activity contract.TurnActivity, event contract.Event) {
	l.activityMu.Lock()
	defer l.activityMu.Unlock()
	l.mu.Lock()
	l.activityCount++
	l.sessions[activity.Session] = sessionState{activity: activity, activityOrder: l.activityCount}
	l.mu.Unlock()
	l.Publish(room, event, false)
}

// Activity returns the last activity and its global order, or zero values.
func (l *Live) Activity(session contract.SessionID) (contract.TurnActivity, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.sessions[session]
	return state.activity, state.activityOrder
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
