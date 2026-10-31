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
