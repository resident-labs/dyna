package live

import (
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestShellOutputDropsOnlySlowWatcher(t *testing.T) {
	l := New(nil)
	s, ok := l.CreateSession("session")
	if !ok || !s.Lock() {
		t.Fatal("session refused")
	}
	s.SetChannel(NewChannel(func() {}, make(chan struct{})))
	s.Unlock()
	slow, problem := s.WatchShell(true)
	if problem != nil {
		t.Fatal(problem)
	}
	fast, problem := s.WatchShell(true)
	if problem != nil {
		t.Fatal(problem)
	}
	if !s.Lock() {
		t.Fatal("session closed")
	}
	generation := s.ShellWatchGeneration()
	s.Unlock()
	for i := 0; i < 128; i++ {
		s.ShellOutput(generation, []contract.ShellJobOutput{{Text: "buffered"}})
		select {
		case <-fast.Events():
		default:
			t.Fatal("fast watcher missed buffered output")
		}
	}
	s.ShellOutput(generation, []contract.ShellJobOutput{{Text: "new"}})
	select {
	case got := <-fast.Events():
		if got.Text != "new" {
			t.Fatalf("fast watcher: %q", got.Text)
		}
	default:
		t.Fatal("fast watcher missed new output")
	}
	for i := 0; i < 128; i++ {
		select {
		case <-slow.Events():
		default:
			t.Fatal("slow watcher lost buffered output")
		}
	}
	select {
	case _, open := <-slow.Events():
		if open {
			t.Fatal("slow watcher remained open")
		}
	default:
		t.Fatal("slow watcher remained open")
	}
	s.RemoveShellWatch(slow)
	if !s.Lock() {
		t.Fatal("session closed")
	}
	s.ClearShell()
	s.Unlock()
	select {
	case _, open := <-fast.Events():
		if open {
			t.Fatal("clear left fast watcher open")
		}
	default:
		t.Fatal("clear left fast watcher open")
	}
}

func TestShellWatchCloseRacesCancellation(t *testing.T) {
	l := New(nil)
	s, ok := l.CreateSession("session")
	if !ok || !s.Lock() {
		t.Fatal("session refused")
	}
	s.SetChannel(NewChannel(func() {}, make(chan struct{})))
	s.Unlock()
	watch, problem := s.WatchShell(true)
	if problem != nil {
		t.Fatal(problem)
	}
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)
	go func() { defer done.Done(); <-start; s.RemoveShellWatch(watch) }()
	go func() {
		defer done.Done()
		<-start
		retired := l.RetireSession("session")
		if retired != nil {
			retired.ClearShell()
			retired.Unlock()
		}
	}()
	close(start)
	done.Wait()
	select {
	case _, open := <-watch.Events():
		if open {
			t.Fatal("watcher remained open")
		}
	default:
		t.Fatal("watcher remained open")
	}
	if _, problem := s.WatchShell(true); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("retired watcher: %v", problem)
	}
}
