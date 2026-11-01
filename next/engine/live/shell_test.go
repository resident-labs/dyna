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
		<-fast.Events()
	}
	s.ShellOutput(generation, []contract.ShellJobOutput{{Text: "new"}})
	if got := <-fast.Events(); got.Text != "new" {
		t.Fatalf("fast watcher: %q", got.Text)
	}
	for i := 0; i < 128; i++ {
		<-slow.Events()
	}
	if _, open := <-slow.Events(); open {
		t.Fatal("slow watcher remained open")
	}
	s.RemoveShellWatch(slow)
	s.ClearShell()
	if _, open := <-fast.Events(); open {
		t.Fatal("clear left fast watcher open")
	}
}
