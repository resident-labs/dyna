package terminalsvc

import (
	"testing"
	"time"
)

func TestConcurrentCloseWaitsForNativeCompletion(t *testing.T) {
	// A closed terminal can still be draining its native process. Every closer
	// must wait for done; removing the row early would release login gates.
	term := &terminal{closed: true, done: make(chan struct{})}
	s := &Service{terms: map[string]*terminal{"login": term}}
	results := make(chan error, 2)
	go func() { results <- s.Close("login") }()
	go func() { results <- s.Close("login") }()
	select {
	case err := <-results:
		t.Fatal("close returned before native completion", err)
	case <-time.After(20 * time.Millisecond):
	}
	s.mu.Lock()
	retained := s.terms["login"] == term
	s.mu.Unlock()
	if !retained {
		t.Fatal("draining terminal vanished")
	}
	close(term.done)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("close did not finish")
		}
	}
}
