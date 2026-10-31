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

// An older activity that arrives late cannot replace the newest label.
func TestPublishActivityKeepsNewest(t *testing.T) {
	l := New(func(map[contract.SessionID]bool) {})
	_, events := l.Subscribe("r", 2)
	newer := contract.Event{Body: contract.TurnActivity{Session: "s", Label: "new"}}
	older := contract.Event{Body: contract.TurnActivity{Session: "s", Label: "old"}}
	l.PublishActivity("r", "s", 2, newer)
	l.PublishActivity("r", "s", 1, older)
	if got := (<-events).Body.(contract.TurnActivity).Label; got != "new" {
		t.Fatalf("label: %s", got)
	}
	select {
	case event := <-events:
		t.Fatalf("stale activity published: %+v", event)
	default:
	}
}
