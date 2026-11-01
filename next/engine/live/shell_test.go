package live

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestShellOutputDropsOnlySlowWatcher(t *testing.T) {
	l := New(nil)
	s, ok := l.CreateSession("session")
	if !ok || !s.Lock() {
		t.Fatal("session refused")
	}
	defer s.Unlock()
	s.SetChannel(NewChannel(func() {}, make(chan struct{})))
	slow := s.WatchShell()
	fast := s.WatchShell()
	generation := s.ShellWatchGeneration()
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
	s.ClearShell()
	select {
	case _, open := <-fast.Events():
		if open {
			t.Fatal("clear left fast watcher open")
		}
	default:
		t.Fatal("clear left fast watcher open")
	}
}
