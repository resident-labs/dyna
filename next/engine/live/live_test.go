package live

import (
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// A full owner stream can close while another publisher holds its copied handle.
func TestConcurrentPublishClosesFullOwnerStream(t *testing.T) {
	l := New(func(map[contract.SessionID]bool) {})
	_, events := l.Subscribe("", 1)
	events <- contract.Event{}
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)
	go func() { defer done.Done(); <-start; l.Publish("r", contract.Event{}, true) }()
	go func() { defer done.Done(); <-start; l.Publish("r", contract.Event{}, false) }()
	close(start)
	done.Wait()
	<-events
	if _, ok := <-events; ok {
		t.Fatal("full owner stream did not close")
	}
}

// A full room stream closes when it drops a read position, so the client reconnects and reads the stored position.
func TestPublishClosesFullRoomStreamOnDroppedRead(t *testing.T) {
	l := New(func(map[contract.SessionID]bool) {})
	_, events := l.Subscribe("r", 1)
	events <- contract.Event{}
	l.Publish("r", contract.Event{Body: contract.ConversationRead{Room: "r", Seq: 10}}, false)
	<-events
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("full room stream delivered a dropped event")
		}
	default:
		t.Fatal("full room stream stayed open after dropping a read position")
	}
}

// Concurrent updates reach streams in the same order that room lists use.
func TestActivityPublicationOrder(t *testing.T) {
	l := New(func(map[contract.SessionID]bool) {})
	_, events := l.Subscribe("r", 2)
	start := make(chan struct{})
	var done sync.WaitGroup
	for _, session := range []contract.SessionID{"a", "b"} {
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			body := contract.TurnActivity{Session: session, Label: string(session)}
			l.PublishActivity("r", body, contract.Event{Body: body})
		}()
	}
	close(start)
	done.Wait()
	first := (<-events).Body.(contract.TurnActivity)
	last := (<-events).Body.(contract.TurnActivity)
	gotFirst, firstOrder := l.Activity(first.Session)
	gotLast, lastOrder := l.Activity(last.Session)
	if gotFirst != first || gotLast != last || firstOrder >= lastOrder {
		t.Fatalf("activity order disagrees with stream: %v (%d), %v (%d)", gotFirst, firstOrder, gotLast, lastOrder)
	}
}
